package positiveint

import "testing"

func TestParsePositiveAcceptsOne(t *testing.T) {
	value, err := ParsePositive("1")
	if err != nil || value != 1 {
		t.Fatalf("positive-int fixture: one rejected: value=%d error=%v", value, err)
	}
}

func TestParsePositiveRejectsInvalidValues(t *testing.T) {
	for _, raw := range []string{"0", "-1", "invalid", "", "1.5"} {
		if _, err := ParsePositive(raw); err == nil {
			t.Errorf("accepted invalid positive integer %q", raw)
		}
	}
}

func TestParsePositiveAcceptsLargerValues(t *testing.T) {
	for _, raw := range []string{"2", "32", "999"} {
		if value, err := ParsePositive(raw); err != nil || value < 2 {
			t.Errorf("rejected valid positive integer %q: value=%d error=%v", raw, value, err)
		}
	}
}
