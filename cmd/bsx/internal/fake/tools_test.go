package fake_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/jmmshn/bees/cmd/bsx/internal/fake"
	"github.com/jmmshn/bees/cmd/bsx/internal/port"
)

func TestFakeGHAnswersPRViewAndChecks(t *testing.T) {
	url := "https://github.com/o/r/pull/1"
	gh := &fake.GH{PRs: map[string]*fake.PR{url: {URL: url, Author: "jmmshn", Head: "toy-1", Base: "main", State: "OPEN", Draft: true}}}
	ctx := context.Background()
	out, err := gh.Run(ctx, "", "gh", "pr", "view", url, "--json", "state,isDraft,author")
	var v struct {
		State   string
		IsDraft bool
		Author  struct{ Login string }
	}
	if err != nil || json.Unmarshal(out, &v) != nil || v.State != "OPEN" || !v.IsDraft || v.Author.Login != "jmmshn" {
		t.Fatalf("view: %s, %v", out, err)
	}
	var ee *port.ExitError
	if _, err := gh.Run(ctx, "", "gh", "pr", "checks", url, "--json", "name,state"); !errors.As(err, &ee) {
		t.Fatalf("no checks registered: %v", err)
	}
	if _, err := gh.Run(ctx, "", "gh", "pr", "view", url+"9", "--json", "state"); !errors.As(err, &ee) {
		t.Fatalf("unknown PR: %v", err)
	}
	gh.Offline = true
	if _, err := gh.Run(ctx, "", "gh", "pr", "view", url, "--json", "state"); err == nil {
		t.Fatal("offline gh answered")
	}
}

func TestFakeCLUploadsAndSubmits(t *testing.T) {
	cl := &fake.CL{}
	ctx := context.Background()
	out, err := cl.Run(ctx, "/w", "cl", "upload")
	id := string(out[:len(out)-1])
	if err != nil || id != "cl/1" {
		t.Fatalf("upload: %q, %v", out, err)
	}
	if _, err := cl.Run(ctx, "/w", "cl", "submit", id); err != nil {
		t.Fatal(err)
	}
	out, _ = cl.Run(ctx, "/w", "cl", "status", id)
	var ch fake.CLChange
	if err := json.Unmarshal(out, &ch); err != nil || ch.State != "submitted" || ch.Dir != "/w" {
		t.Fatalf("status: %s", out)
	}
}
