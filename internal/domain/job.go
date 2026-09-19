package domain

import "time"

type WorkflowJob struct {
	ID          string
	Type        string
	DedupKey    string
	PayloadJSON string
	Status      string
	Attempts    int
	MaxAttempts int
	AvailableAt time.Time
	LeaseOwner  string
	LeaseUntil  time.Time
	LastError   string
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

const (
	JobTypeTriage = "triage"
	JobQueued     = "queued"
	JobRunning    = "running"
	JobSucceeded  = "succeeded"
	JobFailed     = "failed"
	JobDeadLetter = "dead_letter"
)
