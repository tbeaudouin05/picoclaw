package evolution_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/sipeed/picoclaw/pkg/evolution"
)

func TestCanonicalWorkspace_AliasesDedupeButDistinctPathsDoNot(t *testing.T) {
	t.Setenv("HOME", "/root")

	absolute, err := evolution.CanonicalWorkspace("/root/.picoclaw/workspace")
	if err != nil {
		t.Fatalf("CanonicalWorkspace(absolute): %v", err)
	}

	aliases := []string{
		"~/.picoclaw/workspace",
		"/root/.picoclaw/workspace/",
		"/root/.picoclaw/./workspace",
		"/root/.picoclaw/nested/../workspace",
		"  /root/.picoclaw/workspace  ",
	}
	for _, alias := range aliases {
		got, err := evolution.CanonicalWorkspace(alias)
		if err != nil {
			t.Fatalf("CanonicalWorkspace(%q): %v", alias, err)
		}
		if got != absolute {
			t.Fatalf("alias %q canonicalized to %q, want %q", alias, got, absolute)
		}
	}

	// Intentionally distinct paths must stay distinct.
	other, err := evolution.CanonicalWorkspace("/root/.picoclaw/workspace2")
	if err != nil {
		t.Fatalf("CanonicalWorkspace(other): %v", err)
	}
	if other == absolute {
		t.Fatalf("distinct workspace collapsed to %q", absolute)
	}
}

func TestCanonicalWorkspace_RelativeBecomesAbsoluteAndTildeUserPreserved(t *testing.T) {
	t.Setenv("HOME", "/home/tester")

	rel, err := evolution.CanonicalWorkspace("relative/workspace")
	if err != nil {
		t.Fatalf("CanonicalWorkspace(relative): %v", err)
	}
	if !filepath.IsAbs(rel) {
		t.Fatalf("relative path did not become absolute: %q", rel)
	}

	// "~otheruser" is not a home alias and must not be expanded.
	other, err := evolution.CanonicalWorkspace("~otheruser/workspace")
	if err != nil {
		t.Fatalf("CanonicalWorkspace(~otheruser): %v", err)
	}
	if !strings.Contains(other, "~otheruser") {
		t.Fatalf("~otheruser was unexpectedly expanded: %q", other)
	}
}

func TestCanonicalWorkspace_EmptyAndHomeLookupFailure(t *testing.T) {
	if got, err := evolution.CanonicalWorkspace("   "); err != nil || got != "" {
		t.Fatalf("CanonicalWorkspace(blank) = %q, %v; want \"\", nil", got, err)
	}

	// With no discoverable home directory, expanding a leading ~ must fail rather
	// than silently resolving to a wrong relative path.
	t.Setenv("HOME", "")
	if _, err := evolution.CanonicalWorkspace("~/.picoclaw/workspace"); err == nil {
		t.Fatal("expected error when home directory cannot be resolved")
	}
}
