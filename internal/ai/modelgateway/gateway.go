package modelgateway

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Cyaside/Triovexa/internal/ai"
	"github.com/Cyaside/Triovexa/internal/ai/admission"
	"github.com/Cyaside/Triovexa/internal/secretstore"
)

type Config struct {
	Provider                                            ai.ProviderConfig
	Pricing                                             admission.Pricing
	CampaignID, CaseID, AttemptID, Phase, ConfigVersion string
	MaxInputTokens                                      int64
	MaxOutputTokens                                     int
	MaxRequests                                         int
	Cipher                                              *secretstore.Cipher
	Fence                                               func(context.Context) error
	AllowedTools                                        []string
}

type Gateway struct {
	config     Config
	ledger     *admission.Service
	client     *http.Client
	capability string
	mu         sync.Mutex
}

// Envelope and cipher encoding each expand bytes by roughly 4/3. This bound
// leaves room below the durable ledger's 256 KiB encrypted receipt limit.
const maxProviderResponseBytes = 128 * 1024

// ErrContextLimit rejects the final serialized prompt before any reservation or
// provider dispatch. It is distinct from the shared campaign's cumulative cap.
var ErrContextLimit = errors.New("CONTEXT_LIMIT")

func New(config Config, ledger *admission.Service) (*Gateway, error) {
	if err := validateInputContract(config.Pricing); err != nil {
		return nil, err
	}
	if config.Phase == "reviewer" && (len(config.AllowedTools) != 1 || config.AllowedTools[0] != "read_file") {
		return nil, errors.New("reviewer gateway must be read-only")
	}
	for _, name := range config.AllowedTools {
		if !allowedTools[name] {
			return nil, errors.New("gateway tool scope is invalid")
		}
	}
	if config.AllowedTools != nil {
		config.AllowedTools = append([]string{}, config.AllowedTools...)
	}
	validator, err := ai.NewOpenAICompatibleClient(config.Provider)
	if err != nil || !validator.Configured() || ledger == nil || config.Fence == nil || config.Cipher == nil ||
		config.CampaignID == "" || config.AttemptID == "" || config.ConfigVersion == "" || config.Phase == "" ||
		config.MaxInputTokens < 1 || config.MaxOutputTokens < 1 || config.MaxRequests < 1 {
		return nil, errors.New("private model gateway requires a pinned provider, ledger, cipher and fence")
	}
	if config.Provider.Timeout <= 0 {
		config.Provider.Timeout = 5 * time.Minute
	}
	var capability [32]byte
	if _, err := rand.Read(capability[:]); err != nil {
		return nil, err
	}
	return &Gateway{config: config, ledger: ledger, capability: base64.RawURLEncoding.EncodeToString(capability[:]),
		client: &http.Client{Timeout: config.Provider.Timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (g *Gateway) Capability() string { return g.capability }

// Listen binds a fresh loopback socket; no routes are registered in the public
// API server. Cancellation revokes both capability and ongoing model requests.
func (g *Gateway) Listen(ctx context.Context) (string, func(), error) {
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return "", nil, err
	}
	listenCtx, cancel := context.WithCancel(ctx)
	server := &http.Server{Handler: g, ReadHeaderTimeout: 5 * time.Second, BaseContext: func(net.Listener) context.Context { return listenCtx }}
	go func() { _ = server.Serve(listener) }()
	var closeOnce sync.Once
	closeServer := func() {
		closeOnce.Do(func() { cancel(); _ = server.Close() })
	}
	watcherDone := make(chan struct{})
	go func() {
		defer close(watcherDone)
		<-listenCtx.Done()
		closeServer()
	}()
	stop := func() { closeServer(); <-watcherDone }
	return "http://" + listener.Addr().String() + "/v1", stop, nil
}

func (g *Gateway) ServeHTTP(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost || request.URL.Path != "/v1/chat/completions" || request.URL.RawQuery != "" {
		http.Error(writer, "private route unavailable", http.StatusNotFound)
		return
	}
	if request.Header.Get("Origin") != "" || request.Header.Get("X-Triovexa-Case-ID") != g.config.CaseID || request.Header.Get("X-Triovexa-Attempt-ID") != g.config.AttemptID ||
		subtle.ConstantTimeCompare([]byte(request.Header.Get("Authorization")), []byte("Bearer "+g.capability)) != 1 {
		http.Error(writer, "private capability invalid", http.StatusForbidden)
		return
	}
	ordinal, err := strconv.Atoi(request.Header.Get("X-Triovexa-Request-Ordinal"))
	if err != nil || ordinal < 1 || ordinal > g.config.MaxRequests {
		http.Error(writer, "request ordinal invalid", http.StatusConflict)
		return
	}
	preview := request.Header.Values("X-Triovexa-Context-Preview")
	if len(preview) > 1 || (len(preview) == 1 && preview[0] != "1") {
		http.Error(writer, "context preview invalid", http.StatusConflict)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(writer, request.Body, 256*1024))
	if err != nil {
		http.Error(writer, "model payload exceeds bound", http.StatusRequestEntityTooLarge)
		return
	}
	if len(preview) == 1 {
		result, err := g.PreviewInput(request.Context(), body)
		if err != nil {
			http.Error(writer, denialCode(err), http.StatusConflict)
			return
		}
		writer.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(writer).Encode(result)
		return
	}
	response, status, _, err := g.DispatchAt(request.Context(), ordinal, body)
	if err != nil {
		// Do not relay provider text, headers, URL or private reasoning to callers.
		http.Error(writer, denialCode(err), http.StatusConflict)
		return
	}
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_, _ = writer.Write(response)
}

func (g *Gateway) DispatchAt(ctx context.Context, ordinal int, body []byte) ([]byte, int, time.Duration, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if ordinal < 1 || ordinal > g.config.MaxRequests {
		return nil, 0, 0, admission.ErrBudgetExceeded
	}
	if err := g.config.Fence(ctx); err != nil {
		return nil, 0, 0, errors.New("LEASE_LOST")
	}
	bound, err := g.validateInput(body)
	if err != nil {
		return nil, 0, 0, err
	}
	if bound > g.config.MaxInputTokens {
		return nil, 0, 0, ErrContextLimit
	}
	// Preceding ordinal receipts must be final. A new process cannot skip an
	// uncertain request by choosing a fresh ordinal.
	for previous := 1; previous < ordinal; previous++ {
		stored, err := g.ledger.GetRequest(ctx, g.requestID(previous))
		if err != nil || stored.State != admission.Accounted {
			return nil, 0, 0, admission.ErrUncertain
		}
	}
	parsed, _ := url.Parse(g.config.Provider.BaseURL)
	external := parsed.Hostname() != "127.0.0.1" && parsed.Hostname() != "::1" && parsed.Hostname() != "localhost"
	reservation, err := g.ledger.Reserve(ctx, admission.Request{ID: g.requestID(ordinal), CampaignID: g.config.CampaignID,
		AttemptID: g.config.AttemptID, Phase: g.config.Phase, ConfigVersion: g.config.ConfigVersion,
		PayloadHash: admission.PayloadHash(body), Provider: g.config.Provider.Name, Model: g.config.Provider.Model,
		External: external, InputTokenBound: bound, OutputTokenBound: int64(g.config.MaxOutputTokens)}, g.config.Pricing)
	if err != nil {
		return nil, 0, 0, err
	}
	if reservation.State == admission.Accounted && reservation.Receipt != nil {
		if reservation.Receipt.ResponseEncoding != "sealed-v1" {
			return nil, 0, 0, admission.ErrUncertain
		}
		plain, err := g.config.Cipher.Decrypt(string(reservation.Receipt.Response))
		if err != nil {
			return nil, 0, 0, admission.ErrUncertain
		}
		var envelope struct {
			Body []byte `json:"body"`
		}
		if json.Unmarshal([]byte(plain), &envelope) != nil || len(envelope.Body) > maxProviderResponseBytes {
			return nil, 0, 0, admission.ErrUncertain
		}
		return envelope.Body, reservation.Receipt.HTTPStatus, time.Duration(reservation.Receipt.LatencyMS) * time.Millisecond, nil
	}
	if err := g.config.Fence(ctx); err != nil {
		_ = g.ledger.CancelReserved(ctx, reservation.Request.ID, reservation.Request.PayloadHash)
		return nil, 0, 0, errors.New("LEASE_LOST")
	}
	if err := g.ledger.StartDispatch(ctx, reservation.Request.ID, reservation.Request.PayloadHash); err != nil {
		return nil, 0, 0, err
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, completionURL(g.config.Provider.BaseURL), bytes.NewReader(body))
	if err != nil {
		_ = g.ledger.MarkUncertain(context.WithoutCancel(ctx), reservation.Request.ID, reservation.Request.PayloadHash)
		return nil, 0, 0, admission.ErrUncertain
	}
	request.Header.Set("Authorization", "Bearer "+g.config.Provider.APIKey)
	request.Header.Set("Content-Type", "application/json")
	started := time.Now()
	response, err := g.client.Do(request)
	latency := time.Since(started)
	if err != nil {
		_ = g.ledger.MarkUncertain(context.WithoutCancel(ctx), reservation.Request.ID, reservation.Request.PayloadHash)
		return nil, 0, latency, admission.ErrUncertain
	}
	defer response.Body.Close()
	raw, readErr := io.ReadAll(io.LimitReader(response.Body, maxProviderResponseBytes+1))
	if readErr != nil || len(raw) > maxProviderResponseBytes {
		_ = g.ledger.MarkUncertain(context.WithoutCancel(ctx), reservation.Request.ID, reservation.Request.PayloadHash)
		return nil, 0, latency, admission.ErrUncertain
	}
	var decoded struct {
		Model string          `json:"model"`
		Usage json.RawMessage `json:"usage"`
	}
	decodeErr := json.Unmarshal(raw, &decoded)
	usage, usageStatus := ai.ParseCompletionUsage(decoded.Usage)
	known := decodeErr == nil && unambiguousJSON(raw) == nil && usageStatus == ai.UsageKnown && decoded.Model == g.config.Provider.Model && response.StatusCode >= 200 && response.StatusCode < 300
	// The credential cipher trims strings and rejects empty values. A JSON
	// byte envelope preserves response bytes (including empty/error bodies)
	// without changing the credential cipher's stricter secret semantics.
	envelope, _ := json.Marshal(struct {
		Body []byte `json:"body"`
	}{Body: raw})
	sealed, sealErr := g.config.Cipher.Encrypt(string(envelope))
	if sealErr != nil {
		_ = g.ledger.MarkUncertain(context.WithoutCancel(ctx), reservation.Request.ID, reservation.Request.PayloadHash)
		return nil, 0, latency, admission.ErrUncertain
	}
	receipt := admission.Receipt{RequestID: reservation.Request.ID, PayloadHash: reservation.Request.PayloadHash,
		Usage:    admission.Usage{Present: known, PromptTokens: int64(usage.PromptTokens), CompletionTokens: int64(usage.CompletionTokens), TotalTokens: int64(usage.TotalTokens), CachedInputTokens: int64(usage.CachedPromptTokens), ReasoningTokens: int64(usage.ReasoningTokens)},
		Response: []byte(sealed), ResponseEncoding: "sealed-v1", HTTPStatus: response.StatusCode, LatencyMS: latency.Milliseconds()}
	if err := g.ledger.RecordResponse(context.WithoutCancel(ctx), receipt); err != nil {
		return nil, 0, latency, err
	}
	return raw, response.StatusCode, latency, nil
}

func denialCode(err error) string {
	for _, candidate := range []struct {
		err  error
		code string
	}{
		{ErrContextLimit, "CONTEXT_LIMIT"},
		{admission.ErrOffline, "OFFLINE_EGRESS_DENIED"}, {admission.ErrPricingUnknown, "PRICING_UNKNOWN"},
		{admission.ErrBillingUnbounded, "BILLING_UNBOUNDED"}, {admission.ErrBudgetExceeded, "BUDGET_EXHAUSTED"},
		{admission.ErrUncertain, "PROVIDER_DISPATCH_UNCERTAIN"}, {admission.ErrUsageInvalid, "USAGE_UNKNOWN"},
	} {
		if errors.Is(err, candidate.err) {
			return candidate.code
		}
	}
	return "MODEL_DISPATCH_BLOCKED"
}

func (g *Gateway) requestID(ordinal int) string {
	return fmt.Sprintf("%s:%s:%s:%d", g.config.CampaignID, g.config.AttemptID, g.config.Phase, ordinal)
}

func (g *Gateway) Dispatch(ctx context.Context, config ai.ProviderConfig, body []byte) ([]byte, int, time.Duration, error) {
	if strings.TrimRight(config.BaseURL, "/") != strings.TrimRight(g.config.Provider.BaseURL, "/") || config.Model != g.config.Provider.Model || config.Name != g.config.Provider.Name {
		return nil, 0, 0, errors.New("PROVIDER_CHANGED")
	}
	scope, ok := RequestIdentity(ctx)
	if !ok || scope.AttemptID != g.config.AttemptID || scope.Phase != g.config.Phase {
		return nil, 0, 0, errors.New("model call has no stable admitted request identity")
	}
	return g.DispatchAt(ctx, scope.Ordinal, body)
}

type Identity struct {
	AttemptID string
	Phase     string
	Ordinal   int
}
type identityKey struct{}

func WithRequestIdentity(ctx context.Context, identity Identity) context.Context {
	return context.WithValue(ctx, identityKey{}, identity)
}
func RequestIdentity(ctx context.Context) (Identity, bool) {
	identity, ok := ctx.Value(identityKey{}).(Identity)
	return identity, ok
}
