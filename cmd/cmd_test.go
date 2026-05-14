package cmd

import (
	"bytes"
	"strings"
	"testing"
)

// TestCommandTree_MetricsConvertResolves verifies that the
// `statsig-to-ld metrics convert` path is wired correctly through
// the cobra command tree.
func TestCommandTree_MetricsConvertResolves(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"metrics", "convert"})
	if err != nil {
		t.Fatalf("rootCmd.Find([\"metrics\", \"convert\"]) failed: %v", err)
	}
	if c.Name() != "convert" {
		t.Errorf("resolved command name = %q, want %q", c.Name(), "convert")
	}
	if c.Parent() == nil || c.Parent().Name() != "metrics" {
		t.Errorf("parent = %v, want %q", c.Parent(), "metrics")
	}
	if c.Parent().Parent() == nil || c.Parent().Parent().Name() != "statsig-to-ld" {
		t.Errorf("grandparent = %v, want %q", c.Parent().Parent(), "statsig-to-ld")
	}
}

// TestConvertCmd_FlagsBound verifies every user-facing flag is registered
// on convertCmd. Catches flags accidentally dropped during refactoring.
func TestConvertCmd_FlagsBound(t *testing.T) {
	expected := []string{
		"metric", "all", "dry-run",
		"statsig-key", "statsig-url",
		"ld-key", "ld-url", "ld-project",
		"ld-data-source", "source-mapping", "unit-type-mapping",
		"output", "format", "default-unit",
		"include-tags", "include-types", "concurrency", "verbose",
	}
	for _, name := range expected {
		if convertCmd.Flags().Lookup(name) == nil {
			t.Errorf("flag --%s not registered on `metrics convert`", name)
		}
	}
}

// TestAnalyzeCmdResolves verifies `statsig-to-ld analyze` is wired in
// as a top-level command.
func TestAnalyzeCmdResolves(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"analyze"})
	if err != nil {
		t.Fatalf("rootCmd.Find([\"analyze\"]) failed: %v", err)
	}
	if c.Name() != "analyze" {
		t.Errorf("resolved command name = %q, want %q", c.Name(), "analyze")
	}
	if c.Parent() == nil || c.Parent().Name() != "statsig-to-ld" {
		t.Errorf("parent = %v, want root", c.Parent())
	}
}

// TestAnalyzeCmd_FlagsBound verifies every user-facing flag is registered
// on analyzeCmd.
func TestAnalyzeCmd_FlagsBound(t *testing.T) {
	expected := []string{
		"statsig-key", "statsig-url",
		"ld-key", "ld-url", "ld-project",
		"output", "include-tag",
	}
	for _, name := range expected {
		if analyzeCmd.Flags().Lookup(name) == nil {
			t.Errorf("flag --%s not registered on `analyze`", name)
		}
	}
}

// TestCommandTree_FlagsImportResolves verifies the `flags import` path
// is wired through the command tree.
func TestCommandTree_FlagsImportResolves(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"flags", "import"})
	if err != nil {
		t.Fatalf("rootCmd.Find([\"flags\", \"import\"]) failed: %v", err)
	}
	if c.Name() != "import" {
		t.Errorf("resolved command name = %q, want %q", c.Name(), "import")
	}
	if c.Parent() == nil || c.Parent().Name() != "flags" {
		t.Errorf("parent = %v, want %q", c.Parent(), "flags")
	}
	if c.Parent().Parent() == nil || c.Parent().Parent().Name() != "statsig-to-ld" {
		t.Errorf("grandparent = %v, want %q", c.Parent().Parent(), "statsig-to-ld")
	}
}

// TestFlagsImportCmd_FlagsBound verifies every user-facing flag is registered
// on flagsImportCmd.
func TestFlagsImportCmd_FlagsBound(t *testing.T) {
	expected := []string{
		"all", "dry-run", "import-type", "include-tag",
		"ld-tag", "ld-maintainer",
		"statsig-key", "statsig-url",
		"ld-key", "ld-url", "ld-project",
		"output", "format", "concurrency", "verbose",
	}
	for _, name := range expected {
		if flagsImportCmd.Flags().Lookup(name) == nil {
			t.Errorf("flag --%s not registered on `flags import`", name)
		}
	}
}

// TestCommandTree_TargetingImportResolves verifies the `targeting import` path.
func TestCommandTree_TargetingImportResolves(t *testing.T) {
	c, _, err := rootCmd.Find([]string{"targeting", "import"})
	if err != nil {
		t.Fatalf("rootCmd.Find([\"targeting\", \"import\"]) failed: %v", err)
	}
	if c.Name() != "import" {
		t.Errorf("resolved command name = %q, want %q", c.Name(), "import")
	}
	if c.Parent() == nil || c.Parent().Name() != "targeting" {
		t.Errorf("parent = %v, want %q", c.Parent(), "targeting")
	}
}

// TestTargetingImportCmd_FlagsBound verifies every user-facing flag is
// registered on targetingImportCmd.
func TestTargetingImportCmd_FlagsBound(t *testing.T) {
	expected := []string{
		"all", "dry-run", "import-type", "include-tag",
		"ld-tag", "accept-data-loss", "no-create-envs",
		"statsig-key", "statsig-url",
		"ld-key", "ld-url", "ld-project",
		"output", "format", "verbose",
	}
	for _, name := range expected {
		if targetingImportCmd.Flags().Lookup(name) == nil {
			t.Errorf("flag --%s not registered on `targeting import`", name)
		}
	}
}

// TestParseAcceptDataLoss exercises the --accept-data-loss value parser.
func TestParseAcceptDataLoss(t *testing.T) {
	cases := []struct {
		name      string
		in        string
		wantAll   bool
		wantSet   []string
		wantErr   bool
	}{
		{"empty = strict", "", false, nil, false},
		{"all keyword", "all", true, nil, false},
		{"single feature", "segments", false, []string{"segments"}, false},
		{"multiple features", "segments,prerequisites", false, []string{"segments", "prerequisites"}, false},
		{"whitespace tolerant", "segments, prerequisites", false, []string{"segments", "prerequisites"}, false},
		{"unknown feature errors", "bogus", false, nil, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			set, all, err := parseAcceptDataLoss(tc.in)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr=%v", err, tc.wantErr)
			}
			if tc.wantErr {
				return
			}
			if all != tc.wantAll {
				t.Errorf("acceptAll = %v, want %v", all, tc.wantAll)
			}
			for _, f := range tc.wantSet {
				if !set[f] {
					t.Errorf("feature %q not in accepted set: %v", f, set)
				}
			}
		})
	}
}

// TestHelp_AllLevels verifies --help renders without error at every
// level of the command tree. Smoke-tests the wiring end-to-end.
func TestHelp_AllLevels(t *testing.T) {
	levels := [][]string{
		{"--help"},
		{"metrics", "--help"},
		{"metrics", "convert", "--help"},
		{"analyze", "--help"},
		{"flags", "--help"},
		{"flags", "import", "--help"},
		{"targeting", "--help"},
		{"targeting", "import", "--help"},
	}
	for _, args := range levels {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			var buf bytes.Buffer
			rootCmd.SetOut(&buf)
			rootCmd.SetErr(&buf)
			rootCmd.SetArgs(args)
			if err := rootCmd.Execute(); err != nil {
				t.Fatalf("rootCmd.Execute(%v) failed: %v", args, err)
			}
			if buf.Len() == 0 {
				t.Errorf("rootCmd.Execute(%v) produced empty output", args)
			}
		})
	}
}
