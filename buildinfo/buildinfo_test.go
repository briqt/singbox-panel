package buildinfo

import "testing"

// An unstamped build must still identify itself. Reporting an empty version
// would make "which build is live?" unanswerable in exactly the case where the
// answer matters most — someone ran a bare `go build` and deployed it.
func TestGetNeverReturnsBlankIdentity(t *testing.T) {
	info := Get()
	if info.Version == "" {
		t.Fatal("version is empty; a build must always name itself")
	}
	if info.Commit == "" {
		t.Fatal("commit is empty; a build must always name itself")
	}
}

func TestWithDefaultsFillsOnlyMissingFields(t *testing.T) {
	got := withDefaults(Info{Version: "v1.2.3", Commit: "abc123def456"})
	if got.Version != "v1.2.3" || got.Commit != "abc123def456" {
		t.Fatalf("defaults overwrote a stamped build: %+v", got)
	}

	got = withDefaults(Info{})
	if got.Version != "dev" || got.Commit != "unknown" {
		t.Fatalf("unstamped build did not fall back: %+v", got)
	}
}

// The commit is a display value; a full 40-character hash is noise in a status
// line, but it must stay long enough to identify a commit unambiguously.
func TestShortCommitTruncatesLongRevisions(t *testing.T) {
	if got := shortCommit("0123456789abcdef0123456789abcdef01234567"); got != "0123456789ab" {
		t.Fatalf("shortCommit=%q", got)
	}
	if got := shortCommit("abc123"); got != "abc123" {
		t.Fatalf("a short revision must pass through unchanged, got %q", got)
	}
}
