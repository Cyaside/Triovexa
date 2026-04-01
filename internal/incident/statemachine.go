package incident

import "github.com/Cyaside/Triovexa/internal/domain"

var allowedTransitions = map[domain.IncidentState]map[domain.IncidentState]struct{}{
	domain.IncidentStateDetected: {
		domain.IncidentStateTriaging: {},
		domain.IncidentStateClosed:   {},
	},
	domain.IncidentStateTriaging: {
		domain.IncidentStateActionProposed: {},
		domain.IncidentStateEscalated:      {},
		domain.IncidentStateClosed:         {},
	},
	domain.IncidentStateActionProposed: {
		domain.IncidentStateAwaitingApproval: {},
		domain.IncidentStateApproved:         {},
		domain.IncidentStateEscalated:        {},
		domain.IncidentStateClosed:           {},
	},
	domain.IncidentStateAwaitingApproval: {
		domain.IncidentStateApproved:  {},
		domain.IncidentStateEscalated: {},
		domain.IncidentStateClosed:    {},
	},
	domain.IncidentStateApproved: {
		domain.IncidentStateExecutingAction: {},
		domain.IncidentStateEscalated:       {},
	},
	domain.IncidentStateExecutingAction: {
		domain.IncidentStateVerifyingAction:   {},
		domain.IncidentStateFailedRemediation: {},
		domain.IncidentStateEscalated:         {},
	},
	domain.IncidentStateVerifyingAction: {
		domain.IncidentStateResolved:          {},
		domain.IncidentStateFailedRemediation: {},
		domain.IncidentStateRolledBack:        {},
		domain.IncidentStateEscalated:         {},
	},
	domain.IncidentStateResolved: {
		domain.IncidentStateClosed: {},
	},
	domain.IncidentStateFailedRemediation: {
		domain.IncidentStateRolledBack: {},
		domain.IncidentStateEscalated:  {},
		domain.IncidentStateClosed:     {},
	},
	domain.IncidentStateRolledBack: {
		domain.IncidentStateEscalated: {},
		domain.IncidentStateClosed:    {},
	},
	domain.IncidentStateEscalated: {
		domain.IncidentStateClosed: {},
	},
}

func CanTransition(from, to domain.IncidentState) bool {
	nextStates, ok := allowedTransitions[from]
	if !ok {
		return false
	}

	_, ok = nextStates[to]
	return ok
}
