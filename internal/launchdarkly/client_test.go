package launchdarkly

import (
	"strings"
	"testing"
)

func TestActionableHint_UnitNotFound(t *testing.T) {
	msg := `Randomization unit "stableid" not found in project settings [user]`
	hint := actionableHint(400, msg)
	if hint == "" {
		t.Fatal("expected actionable hint for unit-not-found 400, got empty string")
	}
	if !strings.Contains(hint, "stableid") {
		t.Errorf("hint should name the missing unit %q, got: %s", "stableid", hint)
	}
	if !strings.Contains(hint, "--unit-type-mapping") {
		t.Errorf("hint should reference the --unit-type-mapping flag, got: %s", hint)
	}
}

func TestActionableHint_OtherErrors(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		msg        string
	}{
		{"unrelated 400", 400, "some other validation error"},
		{"401 unauthorized", 401, "unauthorized"},
		{"500 server error", 500, "internal server error"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if hint := actionableHint(tc.statusCode, tc.msg); hint != "" {
				t.Errorf("expected no hint, got: %s", hint)
			}
		})
	}
}
