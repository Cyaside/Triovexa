package observability

import (
	"strings"

	"github.com/Cyaside/Triovexa/internal/domain"
)

// QueryRenderer is shared by initial evidence collection and post-action
// verification so both phases query the same service/environment boundary.
type QueryRenderer struct{}

func NewQueryRenderer() *QueryRenderer { return &QueryRenderer{} }

func (r *QueryRenderer) Render(template string, incident domain.Incident) string {
	values := map[string]string{
		"{{service}}":     escapeQueryLabel(incident.ServiceName),
		"{{environment}}": escapeQueryLabel(incident.Environment),
		"{{severity}}":    escapeQueryLabel(incident.Severity),
		"{{title}}":       escapeQueryLabel(incident.Title),
	}
	result := template
	for placeholder, value := range values {
		result = strings.ReplaceAll(result, placeholder, value)
	}
	return strings.TrimSpace(result)
}

func escapeQueryLabel(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, `"`, `\"`)
	value = strings.ReplaceAll(value, "\n", `\n`)
	value = strings.ReplaceAll(value, "\r", `\r`)
	return value
}

func renderQueryTemplate(template string, incident domain.Incident) string {
	return NewQueryRenderer().Render(template, incident)
}
