package mode

import (
	"fmt"
	"strings"
	"sync"
)

type Reasoning string

const (
	ReasoningHeuristic Reasoning = "heuristic"
	ReasoningMistral   Reasoning = "mistral"
)

type Observability string

const (
	ObservabilityDemo    Observability = "demo"
	ObservabilityGrafana Observability = "grafana"
)

type Snapshot struct {
	Reasoning     Reasoning     `json:"reasoning"`
	Observability Observability `json:"observability"`
}

type Manager struct {
	mu            sync.RWMutex
	reasoning     Reasoning
	observability Observability
}

func NewManager(reasoning string, observability string) *Manager {
	return &Manager{
		reasoning:     normalizeReasoning(reasoning),
		observability: normalizeObservability(observability),
	}
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()

	return Snapshot{
		Reasoning:     m.reasoning,
		Observability: m.observability,
	}
}

func (m *Manager) SetReasoning(value string) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.reasoning = normalizeReasoning(value)
	return Snapshot{
		Reasoning:     m.reasoning,
		Observability: m.observability,
	}
}

func (m *Manager) SetObservability(value string) Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.observability = normalizeObservability(value)
	return Snapshot{
		Reasoning:     m.reasoning,
		Observability: m.observability,
	}
}

func ParseReasoning(value string) (Reasoning, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(ReasoningHeuristic):
		return ReasoningHeuristic, nil
	case string(ReasoningMistral):
		return ReasoningMistral, nil
	default:
		return "", fmt.Errorf("unsupported reasoning mode %q", value)
	}
}

func ParseObservability(value string) (Observability, error) {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(ObservabilityDemo):
		return ObservabilityDemo, nil
	case string(ObservabilityGrafana):
		return ObservabilityGrafana, nil
	default:
		return "", fmt.Errorf("unsupported observability mode %q", value)
	}
}

func normalizeReasoning(value string) Reasoning {
	parsed, err := ParseReasoning(value)
	if err != nil {
		return ReasoningHeuristic
	}
	return parsed
}

func normalizeObservability(value string) Observability {
	parsed, err := ParseObservability(value)
	if err != nil {
		return ObservabilityDemo
	}
	return parsed
}
