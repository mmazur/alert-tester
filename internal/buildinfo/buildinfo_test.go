package buildinfo

import "testing"

func TestCurrentPrefersInjectedValues(t *testing.T) {
	prevRevision, prevModified, prevBuildTime := revision, modified, buildTime
	t.Cleanup(func() {
		revision, modified, buildTime = prevRevision, prevModified, prevBuildTime
	})

	revision = "abc123"
	modified = "true"
	buildTime = "2026-06-05T00:00:00Z"

	info := Current()
	if info.Revision != "abc123" {
		t.Fatalf("revision = %q, want abc123", info.Revision)
	}
	if !info.Dirty {
		t.Fatalf("Dirty = false, want true")
	}
	if info.BuildTime != "2026-06-05T00:00:00Z" {
		t.Fatalf("BuildTime = %q, want injected value", info.BuildTime)
	}
	if info.TreeState() != "dirty" {
		t.Fatalf("TreeState = %q, want dirty", info.TreeState())
	}
}

func TestCurrentDefaultsUnknownRevision(t *testing.T) {
	prevRevision, prevModified, prevBuildTime := revision, modified, buildTime
	t.Cleanup(func() {
		revision, modified, buildTime = prevRevision, prevModified, prevBuildTime
	})

	revision = ""
	modified = ""
	buildTime = ""

	info := Current()
	if info.Revision == "" {
		t.Fatalf("revision should never be empty")
	}
}
