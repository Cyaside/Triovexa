package domain

import "time"

type TriageResult struct {
	ID                string
	IncidentID        string
	Summary           string
	Hypotheses        []string
	BlastRadius       string
	NextSteps         []string
	DraftStatusUpdate string
	ConfidenceNotes   string
	CreatedAt         time.Time
}

type EvidenceItem struct {
	ID           string
	IncidentID   string
	Type         string
	Source       string
	Snippet      string
	Timestamp    time.Time
	MetadataJSON string
}

type DocumentReference struct {
	ID              string
	IncidentID      string
	DocumentTitle   string
	DocumentType    string
	RelevanceReason string
	Snippet         string
}

type ApprovalRecord struct {
	ID                string
	CandidateActionID string
	ApprovedBy        string
	Decision          string
	Note              string
	CreatedAt         time.Time
}

type ExecutionRecord struct {
	ID                string
	CandidateActionID string
	ExecutorType      string
	Status            string
	StartedAt         time.Time
	FinishedAt        time.Time
	ResultJSON        string
}

type VerificationResult struct {
	ID                string
	ExecutionRecordID string
	Status            string
	EvidenceJSON      string
	Notes             string
	CreatedAt         time.Time
}

type AuditEvent struct {
	ID          string
	IncidentID  string
	StepName    string
	Status      string
	DetailsJSON string
	StartedAt   time.Time
	FinishedAt  time.Time
}
