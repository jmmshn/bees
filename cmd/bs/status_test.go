package main

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/harness/claude/claudetest"
	"github.com/jmmshn/bees/internal/logbook"
)

func TestStatusShowsRunningAndUnreadForEachSession(t *testing.T) {
	useTempLogbook(t)
	bs(t, "send", "--as", "queen", "worker-a", "one")
	bs(t, "send", "--as", "queen", "worker-a", "two")
	bs(t, "send", "--as", "queen", "worker-b", "three")
	claudetest.FakeRegistry(t).Add(t, "worker-a", "") // queen and worker-b are not running
	out, stderr, code := bs(t, "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	want := "SESSION   LABEL  STATE        UNREAD\n" + // queen: stopped with nothing unread, so left out
		"worker-a  -      running      2\n" +
		"worker-b  -      not running  1\n"
	if out != want {
		t.Fatalf("status =\n%s\nwant\n%s", out, want)
	}
}

func TestStatusShowsAWorkersLabel(t *testing.T) {
	useTempLogbook(t)
	ctx := context.Background()
	lb, err := logbook.Open(ctx, os.Getenv("BS_LOGBOOK"))
	if err != nil {
		t.Fatal(err)
	}
	_, err = lb.RecordSpawn(ctx, "queen-test", logbook.Spawn{Name: "test-3", Bead: "test-3", Label: "role prompts"}, "brief")
	lb.Close()
	if err != nil {
		t.Fatal(err)
	}
	out, _, _ := bs(t, "status")
	if !strings.Contains(out, "test-3   role prompts  not running  1\n") {
		t.Fatalf("status =\n%s", out)
	}
	if out, _, _ := bs(t, "status", "--json"); !strings.Contains(out, `"label":"role prompts"`) {
		t.Fatalf("status --json = %s", out)
	}
}

func TestStatusJSON(t *testing.T) {
	useTempLogbook(t)
	bs(t, "send", "--as", "queen", "worker-a", "one")
	claudetest.FakeRegistry(t).Add(t, "queen", "")
	out, _, code := bs(t, "status", "--json")
	var got []struct {
		Session string `json:"session"`
		Running bool   `json:"running"`
		Unread  int    `json:"unread"`
	}
	if err := json.Unmarshal([]byte(out), &got); code != 0 || err != nil {
		t.Fatalf("exit %d, %v: %q", code, err, out)
	}
	if len(got) != 2 || got[0].Session != "queen" || !got[0].Running || got[0].Unread != 0 ||
		got[1].Session != "worker-a" || got[1].Running || got[1].Unread != 1 {
		t.Fatalf("status --json = %+v", got)
	}
}

func TestStatusRecordsNoReceipts(t *testing.T) {
	useTempLogbook(t)
	bs(t, "send", "--as", "queen", "worker-a", "hello")
	bs(t, "status")
	if out, _, _ := bs(t, "inbox", "--as", "worker-a"); !strings.Contains(out, "hello") {
		t.Fatalf("inbox after status = %q", out)
	}
}

func TestStatusWithAnEmptyLogbook(t *testing.T) {
	useTempLogbook(t)
	if out, _, code := bs(t, "status"); code != 0 || out != "no running sessions and no unread messages\n" {
		t.Fatalf("exit %d, %q", code, out)
	}
}
