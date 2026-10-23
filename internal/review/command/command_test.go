package command

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jmmshn/bees/internal/checks"
	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/port"
)

func cfg() *config.ReviewCommand {
	return &config.ReviewCommand{
		URL:        `https://cl\.test/(?P<repo>[^/]+)/(?P<number>[0-9]+)`,
		Repo:       []string{"cl", "repo"},
		Changes:    []string{"cl", "changes", "{dir}", "{branch}"},
		View:       []string{"cl", "view", "{url}"},
		CI:         []string{"cl", "ci", "{url}"},
		PostReview: []string{"cl", "review", "{url}", "{marker}"},
		Retarget:   "cl rebase {number} --onto {base}",
	}
}

// answers runs as a Runner: argv[1] → its stdout, or a code: prefix for the exit.
func answers(t *testing.T, m map[string]string) Runner {
	return func(_ context.Context, _ string, argv []string, _ time.Duration) (checks.Result, error) {
		out, ok := m[argv[1]]
		if !ok {
			t.Fatalf("unexpected %q", argv)
		}
		if rest, ok := strings.CutPrefix(out, "code:"); ok {
			return checks.Result{Argv: argv, Code: 1, Output: []byte(rest)}, nil
		}
		return checks.Result{Argv: argv, Stdout: []byte(out)}, nil
	}
}

func host(t *testing.T, m map[string]string) Host {
	h, err := New(cfg(), answers(t, m), nil)
	if err != nil {
		t.Fatal(err)
	}
	return h
}

func TestParseMatchesTheWholeURL(t *testing.T) {
	h := host(t, nil)
	r, err := h.Parse(" https://cl.test/toy/12 ")
	if err != nil || r.URL != "https://cl.test/toy/12" || r.Repo != "toy" {
		t.Fatalf("%+v, %v", r, err)
	}
	for _, bad := range []string{"https://cl.test/toy/12/files", "xhttps://cl.test/toy/12", "https://cl.test/toy/0"} {
		if _, err := h.Parse(bad); err == nil {
			t.Errorf("%s parsed", bad)
		}
	}
}

// null is the host's definite "no such change"; a non-zero exit or output that isn't JSON
// is "couldn't ask", never that (Rule G).
func TestViewNullIsNoChangeAndAFailureIsNot(t *testing.T) {
	ctx, r := context.Background(), port.Ref{URL: "https://cl.test/toy/1", Repo: "toy"}
	if _, err := host(t, map[string]string{"view": "null"}).View(ctx, "", r); !errors.Is(err, port.ErrNoChange) {
		t.Errorf("null: %v", err)
	}
	for _, out := range []string{"code:cl: can't reach the host", "not json", ""} {
		if _, err := host(t, map[string]string{"view": out}).View(ctx, "", r); err == nil || errors.Is(err, port.ErrNoChange) {
			t.Errorf("%q: %v", out, err)
		}
	}
	c, err := host(t, map[string]string{"view": `{"url": "https://cl.test/toy/1", "state": "OPEN", "head_commit": "c1", "base": "trunk", "behind": 2, "extra": 1}`}).View(ctx, "", r)
	if err != nil || c.Number != 1 || c.Repo != "toy" || c.State != port.Open || !c.Editable || c.HeadCommit != "c1" {
		t.Fatalf("%+v, %v", c, err)
	}
	if _, err := host(t, map[string]string{"view": `{"url": "https://cl.test/toy/1", "state": "pending"}`}).View(ctx, "", r); err == nil {
		t.Error("a state bs doesn't know read as one")
	}
}

// Changes drops foreign changes and those before since, sorts newest first, and refuses
// a change with no created time (it couldn't be scoped to the spawn: motif 2).
func TestChangesFiltersAndSorts(t *testing.T) {
	ctx := context.Background()
	h := host(t, map[string]string{"changes": `[
		{"url": "https://cl.test/toy/1", "state": "merged", "created": "2026-01-01T00:00:00Z"},
		{"url": "https://cl.test/toy/2", "state": "open", "created": "2026-03-01T00:00:00Z"},
		{"url": "https://cl.test/toy/3", "state": "open", "created": "2026-02-01T00:00:00Z"},
		{"url": "https://cl.test/toy/4", "state": "open", "foreign": true, "created": "2026-04-01T00:00:00Z"}]`})
	cs, _, err := h.Changes(ctx, port.Where{Repo: "/r", Dir: "/r-WT/w", Branch: "w"}, time.Date(2026, 1, 15, 0, 0, 0, 0, time.UTC))
	if err != nil || len(cs) != 2 || cs[0].Number != 2 || cs[1].Number != 3 {
		t.Fatalf("%+v, %v", cs, err)
	}
	h = host(t, map[string]string{"changes": `[{"url": "https://cl.test/toy/1", "state": "open"}]`})
	if _, _, err := h.Changes(ctx, port.Where{Repo: "/r", Dir: "/d", Branch: "w"}, time.Time{}); err == nil {
		t.Error("a change with no created time was scoped")
	}
	// {branch} with none to fill is refused, never run with "".
	if _, _, err := h.Changes(ctx, port.Where{Repo: "/r", Dir: "/d"}, time.Time{}); err == nil || !strings.Contains(err.Error(), "{branch}") {
		t.Errorf("no branch: %v", err)
	}
}

func TestCIAndRepoAndHints(t *testing.T) {
	ctx := context.Background()
	h := host(t, map[string]string{"ci": `{"checks": [{"name": "lint", "bucket": "pass"}], "required": ["lint", "test"]}`, "repo": `{"repo": "toy", "protected": true}`})
	c := port.Change{URL: "https://cl.test/toy/7", Number: 7}
	cs, err := h.CI(ctx, "", c)
	if err != nil || len(cs) != 1 || cs[0] != (port.CICheck{Name: "lint", Bucket: "pass"}) {
		t.Fatalf("%+v, %v", cs, err)
	}
	if req, err := h.RequiredChecks(ctx, "", c); err != nil || strings.Join(req, ",") != "lint,test" {
		t.Fatalf("%v, %v", req, err)
	}
	if repo, ok, err := h.Repo(ctx, "/r"); repo != "toy" || !ok || err != nil {
		t.Fatalf("%s %v %v", repo, ok, err)
	}
	if p, err := h.Protected(ctx, "/r", "toy"); !p || err != nil {
		t.Fatalf("%v %v", p, err)
	}
	if _, ok, err := host(t, map[string]string{"repo": "null"}).Repo(ctx, "/r"); ok || err != nil {
		t.Fatalf("null repo: %v %v", ok, err)
	}
	if got := h.Retarget(c, "trunk"); got != "cl rebase 7 --onto trunk" {
		t.Errorf("retarget: %s", got)
	}
	if got := h.Create(port.Where{Dir: "/d"}); !strings.Contains(got, "/d") {
		t.Errorf("create: %s", got)
	}
}

// The real runner: argv with only PATH, HOME and LANG, stdout read apart from stderr.
func TestExecReadsStdoutAlone(t *testing.T) {
	dir := t.TempDir()
	cl := filepath.Join(dir, "cl")
	if err := os.WriteFile(cl, []byte("#!/bin/sh\necho noise >&2\nprintf '{\"repo\": \"%s\"}' \"${SECRET:-clean}\"\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	c := cfg()
	c.Repo = []string{cl, "repo"}
	h, err := New(c, nil, func(k string) string {
		return map[string]string{"PATH": os.Getenv("PATH"), "SECRET": "leaked"}[k]
	})
	if err != nil {
		t.Fatal(err)
	}
	if repo, _, err := h.Repo(context.Background(), dir); repo != "clean" || err != nil {
		t.Fatalf("%q, %v", repo, err)
	}
}

func TestPostReviewPassesTheBodyAsAFile(t *testing.T) {
	var got []string
	run := func(_ context.Context, _ string, argv []string, _ time.Duration) (checks.Result, error) {
		got = argv
		return checks.Result{Stdout: []byte(`{"url": "https://cl.test/toy/7#r1"}`)}, nil
	}
	c := cfg()
	c.PostReview = []string{"cl", "review", "{url}", "{body_file}", "{marker}"}
	h, err := New(c, run, nil)
	if err != nil {
		t.Fatal(err)
	}
	u, err := h.PostReview(context.Background(), "", port.Change{URL: "https://cl.test/toy/7"}, "body", "bs:1")
	if err != nil || u != "https://cl.test/toy/7#r1" || len(got) != 5 || got[4] != "bs:1" || !strings.HasPrefix(filepath.Base(got[3]), "bs-review-") {
		t.Fatalf("%s %v %q", u, err, got)
	}
}
