package mode

import (
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

func normalizeReasoning(value string) Reasoning {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(ReasoningMistral):
		return ReasoningMistral
	default:
		return ReasoningHeuristic
	}
}

func normalizeObservability(value string) Observability {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(ObservabilityGrafana):
		return ObservabilityGrafana
	default:
		return ObservabilityDemo
	}
}
