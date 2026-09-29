package memory

import (
	"context"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
)

func TestMemoryStoreCompareAndSwapIncidentStateRejectsStaleState(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now().UTC()
	if err := store.CreateIncident(context.Background(), domain.Incident{ID: "inc-cas", State: domain.IncidentStateTriaging, CreatedAt: now, UpdatedAt: now}); err != nil {
		t.Fatal(err)
	}
	updated, err := store.CompareAndSwapIncidentState(context.Background(), "inc-cas", domain.IncidentStateDetected, domain.IncidentStateResolved)
	if err != nil {
		t.Fatal(err)
	}
	if updated {
		t.Fatal("stale transition unexpectedly succeeded")
	}
	incident, _ := store.GetIncident(context.Background(), "inc-cas")
	if incident.State != domain.IncidentStateTriaging {
		t.Fatalf("state = %q, want triaging", incident.State)
	}
}
