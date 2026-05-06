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
	if !strings.Contains(hint, "JSON file") {
		t.Errorf("hint should clarify that --unit-type-mapping takes a file path, not inline JSON, got: %s", hint)
	}
}

func TestActionableHint_Unauthorized(t *testing.T) {
	hint := actionableHint(401, "unauthorized")
	if hint == "" {
		t.Fatal("expected actionable hint for 401, got empty string")
	}
	if !strings.Contains(hint, "Authorization") {
		t.Errorf("hint should reference where to manage tokens, got: %s", hint)
	}
}

func TestActionableHint_Forbidden(t *testing.T) {
	hint := actionableHint(403, "forbidden")
	if hint == "" {
		t.Fatal("expected actionable hint for 403, got empty string")
	}
	if !strings.Contains(hint, "Writer") {
		t.Errorf("hint should mention Writer role, got: %s", hint)
	}
}

func TestActionableHint_NotFound(t *testing.T) {
	hint := actionableHint(404, "project not found")
	if hint == "" {
		t.Fatal("expected actionable hint for 404, got empty string")
	}
	if !strings.Contains(hint, "--ld-project") {
		t.Errorf("hint should reference --ld-project flag, got: %s", hint)
	}
}

func TestActionableHint_NoHint(t *testing.T) {
	cases := []struct {
		name       string
		statusCode int
		msg        string
	}{
		{"unrelated 400", 400, "some other validation error"},
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
