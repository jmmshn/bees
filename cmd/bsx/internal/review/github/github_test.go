package github_test

import (
	"testing"

	"github.com/jmmshn/bees/cmd/bsx/internal/review/github"
)

// One spelling per PR (motif 3): trailing paths and fragments drop; anything not a PR
// URL is refused.
func TestParseRecordsOneSpelling(t *testing.T) {
	for in, want := range map[string]string{
		"https://github.com/o/r/pull/12":            "https://github.com/o/r/pull/12",
		"https://github.com/o/r/pull/12/files":      "https://github.com/o/r/pull/12",
		"https://GitHub.com/o/r/pull/12#discussion": "https://github.com/o/r/pull/12",
	} {
		if r, err := (github.Host{}).Parse(in); err != nil || r.URL != want || r.Repo != "o/r" {
			t.Errorf("Parse(%q) = %+v, %v; want %s", in, r, err, want)
		}
	}
	for _, bad := range []string{"http://github.com/o/r/pull/1", "https://github.com/o/r/issues/1", "https://gitlab.com/o/r/pull/1", "https://github.com/o/r/pull/x", "#12"} {
		if _, err := (github.Host{}).Parse(bad); err == nil {
			t.Errorf("Parse(%q) accepted", bad)
		}
	}
}

func TestRepoOfReadsEveryRemoteForm(t *testing.T) {
	for in, want := range map[string]string{
		"git@github.com:o/r.git": "o/r", "https://github.com/o/r": "o/r", "https://github.com/o/r.git": "o/r",
		"ssh://git@github.com/o/r.git": "o/r", "/tmp/r.git": "", "git@gitlab.com:o/r.git": "",
	} {
		if got, ok := github.RepoOf(in); got != want || ok != (want != "") {
			t.Errorf("RepoOf(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
}
