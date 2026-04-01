package incident

import (
	"testing"

	"github.com/Cyaside/Triovexa/internal/domain"
)

func TestCanTransition(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		from domain.IncidentState
		to   domain.IncidentState
		want bool
	}{
		{
			name: "detected to triaging",
			from: domain.IncidentStateDetected,
			to:   domain.IncidentStateTriaging,
			want: true,
		},
		{
			name: "triaging to action proposed",
			from: domain.IncidentStateTriaging,
			to:   domain.IncidentStateActionProposed,
			want: true,
		},
		{
			name: "verifying action to resolved",
			from: domain.IncidentStateVerifyingAction,
			to:   domain.IncidentStateResolved,
			want: true,
		},
		{
			name: "action proposed cannot skip to executing",
			from: domain.IncidentStateActionProposed,
			to:   domain.IncidentStateExecutingAction,
			want: false,
		},
		{
			name: "closed cannot re-enter triaging",
			from: domain.IncidentStateClosed,
			to:   domain.IncidentStateTriaging,
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			if got := CanTransition(tt.from, tt.to); got != tt.want {
				t.Fatalf("CanTransition(%q, %q) = %v, want %v", tt.from, tt.to, got, tt.want)
			}
		})
	}
}
