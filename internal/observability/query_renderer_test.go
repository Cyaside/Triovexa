package observability

import (
	"strings"
	"testing"

	"github.com/Cyaside/Triovexa/internal/domain"
)

func TestQueryRendererEscapesIncidentLabels(t *testing.T) {
	rendered := NewQueryRenderer().Render(`queue_backlog{service="{{service}}",environment="{{environment}}"}`,
		domain.Incident{ServiceName: `checkout"} or vector(1) #`, Environment: "staging\nwest"})
	if strings.Contains(rendered, `service="checkout"}`) || !strings.Contains(rendered, `checkout\"}`) {
		t.Fatalf("service label was not escaped: %s", rendered)
	}
	if !strings.Contains(rendered, `staging\nwest`) {
		t.Fatalf("newline was not escaped: %s", rendered)
	}
}
