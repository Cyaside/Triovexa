package incident

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage/memory"
)

func TestAlertInvestigationDispatchPrecedesModelTriage(t *testing.T) {
	for _, dispatchErr := range []error{nil, errors.New("investigation budget unavailable")} {
		store := memory.NewMemoryStore()
		now := time.Now().UTC()
		i := domain.Incident{ID: "automatic-alert", ExternalAlertID: "alert", ServiceName: "worker", Environment: "staging", State: domain.IncidentStateDetected, CreatedAt: now, UpdatedAt: now}
		if err := store.CreateIncident(context.Background(), i); err != nil {
			t.Fatal(err)
		}
		collector := &sequenceCollector{}
		// Nil generators intentionally make any fallthrough to triage fail loudly.
		service := NewService(store, collector, nil, nil, nil, nil)
		calls := 0
		service.WithAutomaticInvestigation(func(ctx context.Context, got domain.Incident) (bool, error) {
			calls++
			evidence, err := store.ListEvidenceItems(ctx, got.ID)
			if err != nil || len(evidence) != 1 || got.State != domain.IncidentStateTriaging {
				t.Fatal("dispatch ran before durable evidence collection")
			}
			return true, dispatchErr
		})
		got, err := service.runReadOnlyTriage(context.Background(), i)
		if !errors.Is(err, dispatchErr) || calls != 1 || collector.calls != 1 || got.State != domain.IncidentStateTriaging {
			t.Fatalf("dispatch fell through or lost error: calls=%d state=%s err=%v", calls, got.State, err)
		}
	}
}
