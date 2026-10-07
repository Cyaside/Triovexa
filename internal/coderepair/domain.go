package coderepair

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"time"
)

type State string

const (
	StateProposed                      State = "proposed"
	StateAwaitingInvestigationApproval State = "awaiting_investigation_approval"
	StateInvestigating                 State = "investigating"
	StatePatchReady                    State = "patch_ready"
	StateAwaitingPublishApproval       State = "awaiting_publish_approval"
	StatePublishing                    State = "publishing"
	StatePROpen                        State = "pr_open"
	StateMerged                        State = "merged"
	StateAwaitingDeployment            State = "awaiting_deployment"
	StateVerifying                     State = "verifying"
	StateRecovered                     State = "recovered"
	StateInconclusive                  State = "inconclusive"
	StateBlocked                       State = "blocked"
	StateFailed                        State = "failed"
	StateCancelled                     State = "cancelled"
	StateClosedWithoutMerge            State = "closed_without_merge"
)

var transitions = map[State]map[State]struct{}{
	StateProposed:                      {StateAwaitingInvestigationApproval: {}, StateCancelled: {}},
	StateAwaitingInvestigationApproval: {StateInvestigating: {}, StateCancelled: {}},
	StateInvestigating:                 {StatePatchReady: {}, StateBlocked: {}, StateFailed: {}, StateCancelled: {}},
	StatePatchReady:                    {StateAwaitingPublishApproval: {}, StateCancelled: {}},
	StateAwaitingPublishApproval:       {StatePublishing: {}, StateCancelled: {}},
	StatePublishing:                    {StatePROpen: {}, StateBlocked: {}, StateFailed: {}},
	StatePROpen:                        {StateMerged: {}, StateClosedWithoutMerge: {}},
	StateMerged:                        {StateAwaitingDeployment: {}},
	StateAwaitingDeployment:            {StateVerifying: {}, StateInconclusive: {}},
	StateVerifying:                     {StateRecovered: {}, StateInconclusive: {}, StateFailed: {}},
	StateBlocked:                       {StateAwaitingInvestigationApproval: {}, StateAwaitingPublishApproval: {}, StateCancelled: {}},
	StateInconclusive:                  {StateVerifying: {}, StateCancelled: {}},
}

func CanTransition(from, to State) bool {
	_, ok := transitions[from][to]
	return ok
}

type RepositoryBinding struct {
	ID                string
	ServiceName       string
	Environment       string
	RepositoryURL     string
	BaseRef           string
	AllowedPaths      []string
	TestRecipes       []string
	ValidationProfile *ValidationProfile
	CredentialRef     string
	Automation        *AutomationPolicy
	PolicyVersion     string
	Enabled           bool
	CreatedAt         time.Time
	UpdatedAt         time.Time
}

func (b RepositoryBinding) Validate() error {
	if b.ID == "" || b.ServiceName == "" || b.Environment == "" || b.RepositoryURL == "" || b.BaseRef == "" || b.PolicyVersion == "" {
		return errors.New("repository binding has missing required fields")
	}
	if err := ValidateRepositoryURL(b.RepositoryURL); err != nil {
		return err
	}
	if !validateBaseRef(b.BaseRef) {
		return errors.New("repository binding has invalid base ref")
	}
	if len(b.AllowedPaths) == 0 || len(b.TestRecipes) == 0 {
		return errors.New("repository binding requires allowed paths and test recipes")
	}
	seenPaths := make(map[string]struct{}, len(b.AllowedPaths))
	if b.Automation != nil {
		if err := b.Automation.Validate(); err != nil {
			return err
		}
	}
	if b.ValidationProfile != nil {
		if err := b.ValidationProfile.Validate(); err != nil {
			return err
		}
		if len(b.TestRecipes) != 1 || b.TestRecipes[0] != b.ValidationProfile.ID {
			return errors.New("binding recipe must match its validation profile")
		}
	}
	if err := ValidateCredentialReference(b.CredentialRef); err != nil {
		return err
	}
	for _, name := range b.AllowedPaths {
		if err := ValidateRepoPath(name); err != nil {
			return err
		}
		if _, exists := seenPaths[name]; exists {
			return errors.New("repository binding contains duplicate allowed path")
		}
		seenPaths[name] = struct{}{}
	}
	seenRecipes := make(map[string]struct{}, len(b.TestRecipes))
	for _, id := range b.TestRecipes {
		if !validateRecipeID(id) {
			return errors.New("repository binding contains invalid test recipe ID")
		}
		if _, exists := seenRecipes[id]; exists {
			return errors.New("repository binding contains duplicate test recipe ID")
		}
		seenRecipes[id] = struct{}{}
	}
	return nil
}

type Case struct {
	ID            string
	IncidentID    string
	BindingID     string
	BaseSHA       string
	DeployedSHA   string
	ScopeDigest   string
	PolicyVersion string
	State         State
	Version       int64
	CreatedBy     string
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

type Attempt struct {
	ID            string
	CaseID        string
	Number        int
	Status        string
	Provider      string
	Model         string
	PromptVersion string
	Runtime       *RuntimeSpec `json:"-"`
	ErrorCode     string
	ErrorMessage  string
	StartedAt     time.Time
	FinishedAt    time.Time
	CreatedAt     time.Time
}

type Approval struct {
	ID            string
	CaseID        string
	CaseVersion   int64
	Phase         string
	ActorID       string
	Decision      string
	ScopeDigest   string
	PolicyVersion string
	ExpiresAt     time.Time
	CreatedAt     time.Time
}

type Event struct {
	ID          string
	CaseID      string
	ActorID     string
	Type        string
	DetailsJSON string
	CreatedAt   time.Time
}

type Artifact struct {
	ID            string
	AttemptID     string
	Kind          string
	ContentSHA256 string
	ArtifactRef   string
	ByteSize      int64
	CreatedAt     time.Time
}

// InvestigationOutcome is recorded under the claimed job's fencing token.
// ReportJSON contains the bounded, sanitized diagnosis and test results.
type InvestigationOutcome struct {
	JobID           string
	LeaseToken      string
	CaseID          string
	AttemptID       string
	ExpectedVersion int64
	State           State
	ErrorCode       string
	ErrorMessage    string
	Patch           []byte
	PatchSHA256     string
	ReportJSON      []byte
	RecordedAt      time.Time
}

type Job struct {
	ID          string
	CaseID      string
	AttemptID   string
	Type        string
	DedupKey    string
	PayloadJSON string
	Status      string
	Attempts    int
	MaxAttempts int
	AvailableAt time.Time
	LeaseOwner  string
	LeaseToken  string
	LeaseUntil  time.Time
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const (
	JobTypeInvestigation = "investigation"
	JobQueued            = "queued"
	JobRunning           = "running"
	JobSucceeded         = "succeeded"
	JobDeadLetter        = "dead_letter"
)

func InvestigationDedupKey(caseID string, attemptNumber int) string {
	return fmt.Sprintf("repair:investigation:%s:%d", caseID, attemptNumber)
}

func ScopeDigest(c Case, b RepositoryBinding) (string, error) {
	if c.ID == "" || c.IncidentID == "" || c.BindingID != b.ID || c.BaseSHA == "" || c.DeployedSHA == "" || c.PolicyVersion != b.PolicyVersion {
		return "", errors.New("repair scope is incomplete or binding policy changed")
	}
	paths := append([]string(nil), b.AllowedPaths...)
	recipes := append([]string(nil), b.TestRecipes...)
	sort.Strings(paths)
	sort.Strings(recipes)
	data, err := json.Marshal(struct {
		CaseID, IncidentID, BindingID, BaseSHA, DeployedSHA string
		RepositoryURL, BaseRef, PolicyVersion               string
		AllowedPaths, TestRecipes                           []string
		ValidationProfile                                   *ValidationProfile `json:",omitempty"`
		CredentialRef                                       string             `json:",omitempty"`
		Automation                                          *AutomationPolicy  `json:",omitempty"`
	}{
		CaseID:            c.ID,
		IncidentID:        c.IncidentID,
		BindingID:         c.BindingID,
		BaseSHA:           c.BaseSHA,
		DeployedSHA:       c.DeployedSHA,
		RepositoryURL:     b.RepositoryURL,
		BaseRef:           b.BaseRef,
		PolicyVersion:     b.PolicyVersion,
		AllowedPaths:      paths,
		TestRecipes:       recipes,
		ValidationProfile: b.ValidationProfile,
		CredentialRef:     b.CredentialRef,
		Automation:        b.Automation,
	})
	if err != nil {
		return "", fmt.Errorf("encode repair scope: %w", err)
	}
	digest := sha256.Sum256(data)
	return hex.EncodeToString(digest[:]), nil
}
