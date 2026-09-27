//go:build repair_regression

package workload

import "testing"

// This test intentionally fails on the fixture's base revision. The future
// repair patch must make it pass; normal CI does not run this build tag.
func TestRepairFixtureAcceptsSchemaTwo(t *testing.T) {
	if err := checkRepairFixture("2", "local"); err != nil {
		t.Fatalf("valid schema 2 must be supported after repair: %v", err)
	}
}
