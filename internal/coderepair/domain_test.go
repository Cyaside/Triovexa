package coderepair

import "testing"

func TestCaseStateTransitions(t *testing.T) {
	allowed := [][2]State{
		{StateProposed, StateAwaitingInvestigationApproval},
		{StateAwaitingInvestigationApproval, StateInvestigating},
		{StateInvestigating, StatePatchReady},
		{StatePatchReady, StateAwaitingPublishApproval},
		{StateAwaitingPublishApproval, StatePublishing},
		{StatePublishing, StatePROpen},
		{StatePROpen, StateMerged},
		{StateMerged, StateAwaitingDeployment},
		{StateAwaitingDeployment, StateVerifying},
		{StateVerifying, StateRecovered},
	}
	for _, pair := range allowed {
		if !CanTransition(pair[0], pair[1]) {
			t.Errorf("transition %s -> %s should be allowed", pair[0], pair[1])
		}
	}
	for _, pair := range [][2]State{
		{StateProposed, StatePublishing},
		{StateInvestigating, StateRecovered},
		{StateRecovered, StateVerifying},
		{StateFailed, StatePatchReady},
		{StateCancelled, StateInvestigating},
	} {
		if CanTransition(pair[0], pair[1]) {
			t.Errorf("transition %s -> %s should be rejected", pair[0], pair[1])
		}
	}
}

func TestScopeDigestChangesWithAuthorizationInputs(t *testing.T) {
	binding := RepositoryBinding{
		ID: "binding-1", ServiceName: "queue-worker", Environment: "staging",
		RepositoryURL: "https://github.com/Cyaside/Triovexa", BaseRef: "main",
		AllowedPaths: []string{"internal/workload"}, TestRecipes: []string{"go-test-workload"}, PolicyVersion: "repair-v1",
	}
	caseRecord := Case{ID: "case-1", IncidentID: "incident-1", BindingID: binding.ID, BaseSHA: "base", DeployedSHA: "deployed", PolicyVersion: binding.PolicyVersion}
	first, err := ScopeDigest(caseRecord, binding)
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*RepositoryBinding){
		func(b *RepositoryBinding) { b.AllowedPaths = []string{"internal"} },
		func(b *RepositoryBinding) { b.TestRecipes = []string{"go-test-all"} },
		func(b *RepositoryBinding) { b.RepositoryURL = "https://github.com/example/other" },
	} {
		changed := binding
		mutate(&changed)
		digest, err := ScopeDigest(caseRecord, changed)
		if err != nil {
			t.Fatal(err)
		}
		if digest == first {
			t.Fatal("scope change did not invalidate digest")
		}
	}
	caseRecord.PolicyVersion = "repair-v2"
	if _, err := ScopeDigest(caseRecord, binding); err == nil {
		t.Fatal("policy version change must invalidate scope")
	}
	caseRecord.PolicyVersion = binding.PolicyVersion
	binding.AllowedPaths = []string{"internal/workload", "internal/coderepair"}
	reordered := binding
	reordered.AllowedPaths = []string{"internal/coderepair", "internal/workload"}
	one, err := ScopeDigest(caseRecord, binding)
	if err != nil {
		t.Fatal(err)
	}
	two, err := ScopeDigest(caseRecord, reordered)
	if err != nil || one != two {
		t.Fatalf("path order changed scope digest: first=%s second=%s err=%v", one, two, err)
	}
}
