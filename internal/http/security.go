package http

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/Cyaside/Triovexa/internal/auth"
	"github.com/Cyaside/Triovexa/internal/config"
	"github.com/Cyaside/Triovexa/internal/domain"
)

const sessionCookieName = "triovexa_session"
const csrfCookieName = "triovexa_csrf"

type identityContextKey struct{}

type requestIdentity struct {
	User    domain.User
	Session domain.Session
}

type loginLimiter struct {
	mu       sync.Mutex
	attempts map[string][]time.Time
}

func newLoginLimiter() *loginLimiter { return &loginLimiter{attempts: make(map[string][]time.Time)} }

func (l *loginLimiter) allow(key string, now time.Time) bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	cutoff := now.Add(-5 * time.Minute)
	items := l.attempts[key][:0]
	for _, item := range l.attempts[key] {
		if item.After(cutoff) {
			items = append(items, item)
		}
	}
	if len(items) >= 5 {
		l.attempts[key] = items
		return false
	}
	l.attempts[key] = append(items, now)
	return true
}

func registerSessionRoutes(mux *http.ServeMux, cfg config.Config, service *auth.Service) {
	if service == nil {
		return
	}
	limiter := newLoginLimiter()
	mux.HandleFunc("/api/v1/session", func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodPost:
			if !limiter.allow(remoteHost(r.RemoteAddr), time.Now()) {
				writeAPIError(w, http.StatusTooManyRequests, "login_rate_limited", "Too many login attempts. Try again later.")
				return
			}
			r.Body = http.MaxBytesReader(w, r.Body, 16<<10)
			var payload struct{ Username, Password string }
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				writeAPIError(w, http.StatusBadRequest, "invalid_request", "Invalid login payload.")
				return
			}
			grant, err := service.Login(r.Context(), payload.Username, payload.Password)
			if err != nil {
				writeAPIError(w, http.StatusUnauthorized, "invalid_credentials", "Invalid username or password.")
				return
			}
			http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: grant.Token, Path: "/", HttpOnly: true, Secure: cfg.SessionSecure, SameSite: http.SameSiteLaxMode, Expires: grant.ExpiresAt})
			http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: grant.CSRFToken, Path: "/", HttpOnly: false, Secure: cfg.SessionSecure, SameSite: http.SameSiteLaxMode, Expires: grant.ExpiresAt})
			writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(grant.User), "csrf_token": grant.CSRFToken, "expires_at": grant.ExpiresAt})
		case http.MethodGet:
			identity, ok := currentIdentity(r.Context())
			if !ok {
				writeAPIError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"user": publicUser(identity.User), "expires_at": identity.Session.ExpiresAt})
		default:
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
		}
	})
	mux.HandleFunc("/api/v1/session/logout", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete && r.Method != http.MethodPost {
			writeAPIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "Method not allowed.")
			return
		}
		cookie, _ := r.Cookie(sessionCookieName)
		if cookie != nil {
			_ = service.Logout(r.Context(), cookie.Value)
		}
		http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", HttpOnly: true, Secure: cfg.SessionSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
		http.SetCookie(w, &http.Cookie{Name: csrfCookieName, Value: "", Path: "/", HttpOnly: false, Secure: cfg.SessionSecure, SameSite: http.SameSiteLaxMode, MaxAge: -1})
		w.WriteHeader(http.StatusNoContent)
	})
}

func remoteHost(address string) string {
	host, _, err := net.SplitHostPort(strings.TrimSpace(address))
	if err == nil && host != "" {
		return host
	}
	return strings.TrimSpace(address)
}

func securityMiddleware(cfg config.Config, service *auth.Service, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/webhooks/grafana" {
			r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
			if secret := strings.TrimSpace(cfg.GrafanaWebhookSecret); secret != "" && !secureEqual(bearerToken(r), secret) {
				writeAPIError(w, http.StatusUnauthorized, "invalid_webhook_credential", "Webhook credential is invalid.")
				return
			}
			next.ServeHTTP(w, r)
			return
		}
		if !cfg.InternalMode() {
			ctx := context.WithValue(r.Context(), identityContextKey{}, requestIdentity{User: domain.User{ID: "local", Username: "local-operator", Role: domain.RoleAdmin}})
			next.ServeHTTP(w, r.WithContext(ctx))
			return
		}
		if publicPath(r.URL.Path, r.Method) {
			next.ServeHTTP(w, r)
			return
		}
		if service == nil {
			writeAPIError(w, http.StatusServiceUnavailable, "authentication_unavailable", "Authentication is unavailable.")
			return
		}
		cookie, err := r.Cookie(sessionCookieName)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
			return
		}
		user, session, err := service.Authenticate(r.Context(), cookie.Value)
		if err != nil {
			writeAPIError(w, http.StatusUnauthorized, "authentication_required", "Sign in to continue.")
			return
		}
		if isMutation(r.Method) {
			if user.Role == domain.RoleViewer {
				writeAPIError(w, http.StatusForbidden, "insufficient_role", "Operator role is required.")
				return
			}
			if adminMutation(r.URL.Path) && user.Role != domain.RoleAdmin {
				writeAPIError(w, http.StatusForbidden, "admin_required", "Administrator role is required.")
				return
			}
			csrf := r.Header.Get("X-CSRF-Token")
			if csrf == "" && strings.Contains(r.Header.Get("Content-Type"), "application/x-www-form-urlencoded") {
				_ = r.ParseForm()
				csrf = r.FormValue("_csrf")
			}
			if !service.ValidateCSRF(session, csrf) {
				writeAPIError(w, http.StatusForbidden, "csrf_invalid", "Refresh the page and try again.")
				return
			}
		}
		ctx := context.WithValue(r.Context(), identityContextKey{}, requestIdentity{User: user, Session: session})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func currentIdentity(ctx context.Context) (requestIdentity, bool) {
	identity, ok := ctx.Value(identityContextKey{}).(requestIdentity)
	return identity, ok
}

func publicPath(path string, method string) bool {
	return path == "/health" || path == "/metrics" || (method == http.MethodGet && strings.HasPrefix(path, "/ui")) || (path == "/api/v1/session" && method == http.MethodPost)
}

func isMutation(method string) bool {
	return method != http.MethodGet && method != http.MethodHead && method != http.MethodOptions
}

func adminMutation(path string) bool {
	return strings.Contains(path, "/admin/") || strings.Contains(path, "/settings") || strings.Contains(path, "/connections") || strings.Contains(path, "/setup/observability")
}

func bearerToken(r *http.Request) string {
	value := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(value) > 7 && strings.EqualFold(value[:7], "Bearer ") {
		return strings.TrimSpace(value[7:])
	}
	return ""
}

func secureEqual(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func publicUser(user domain.User) map[string]any {
	return map[string]any{"id": user.ID, "username": user.Username, "role": user.Role}
}

func writeAPIError(w http.ResponseWriter, status int, code string, message string) {
	writeJSON(w, status, map[string]any{"error": map[string]string{"code": code, "message": message}})
}

func actorFromRequest(r *http.Request, fallback string) string {
	if identity, ok := currentIdentity(r.Context()); ok && identity.User.Username != "" {
		return identity.User.Username
	}
	return fallback
}
