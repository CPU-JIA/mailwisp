package buildinfo

import "testing"

func TestCurrentDevelopmentDefaults(t *testing.T) {
	got := Current()
	want := (Info{
		Name:      "MailWisp",
		Version:   "dev",
		Commit:    "unknown",
		BuildDate: "unknown",
	})
	if got != want {
		t.Fatalf("Current() = %+v, want %+v", got, want)
	}
}

func TestCurrentLinkedIdentity(t *testing.T) {
	originalVersion, originalCommit, originalBuildDate := version, commit, buildDate
	t.Cleanup(func() {
		version, commit, buildDate = originalVersion, originalCommit, originalBuildDate
	})

	version = "1.2.3"
	commit = "0123456789abcdef0123456789abcdef01234567"
	buildDate = "2026-07-17T04:05:06Z"

	got := Current()
	if got.Version != version || got.Commit != commit || got.BuildDate != buildDate {
		t.Fatalf("Current() = %+v, want linked identity", got)
	}
}
