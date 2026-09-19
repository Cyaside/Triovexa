package execution

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/Cyaside/Triovexa/internal/domain"
	"github.com/Cyaside/Triovexa/internal/storage"
)

type countingAdapter struct{ calls atomic.Int32 }

func (a *countingAdapter) Execute(_ context.Context, _ domain.CandidateAction, _ AdapterRequest) (AdapterResult, error) {
	a.calls.Add(1)
	time.Sleep(10 * time.Millisecond)
	return AdapterResult{ExecutorType: "counting", Payload: map[string]any{"ok": true}}, nil
}

func TestExecuteActionClaimsOnceUnderConcurrency(t *testing.T) {
	repository := storage.NewMemoryStore()
	incidentRecord, action := seedApprovedExecutionFixture(t, repository)
	adapter := &countingAdapter{}
	service := NewService(repository, DefaultCatalog(), adapter, nil, nil, time.Second, 0, time.Minute)

	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = service.ExecuteAction(context.Background(), action.ID, "operator")
		}()
	}
	wg.Wait()
	if calls := adapter.calls.Load(); calls != 1 {
		t.Fatalf("adapter calls = %d, want 1 for incident %s", calls, incidentRecord.ID)
	}
}
