package workload

import "testing"

func TestRepairFixtureIsOptInAndLocalOnly(t *testing.T) {
	tests := []struct {
		name    string
		schema  string
		env     string
		wantErr bool
	}{
		{name: "disabled in any environment", env: "internal"},
		{name: "supported schema in local", schema: "1", env: "local"},
		{name: "reject fixture in internal", schema: "1", env: "internal", wantErr: true},
		{name: "reject malformed schema", schema: "not-a-number", env: "local", wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := checkRepairFixture(tt.schema, tt.env)
			if (err != nil) != tt.wantErr {
				t.Fatalf("checkRepairFixture(%q, %q) error = %v, want error %v", tt.schema, tt.env, err, tt.wantErr)
			}
		})
	}
}
