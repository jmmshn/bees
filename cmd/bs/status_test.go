package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jmmshn/bees/internal/harness/claude/claudetest"
)

func TestStatusShowsRunningAndUnreadForEachSession(t *testing.T) {
	useTempLogbook(t)
	bs(t, "send", "--as", "queen", "bee-a", "one")
	bs(t, "send", "--as", "queen", "bee-a", "two")
	bs(t, "send", "--as", "queen", "bee-b", "three")
	claudetest.FakeRegistry(t).Add(t, "bee-a", "") // queen and bee-b are not running
	out, stderr, code := bs(t, "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	want := "SESSION  STATE        UNREAD\n" + // queen: stopped with nothing unread, so left out
		"bee-a    running      2\n" +
		"bee-b    not running  1\n"
	if out != want {
		t.Fatalf("status =\n%s\nwant\n%s", out, want)
	}
}

func TestStatusJSON(t *testing.T) {
	useTempLogbook(t)
	bs(t, "send", "--as", "queen", "bee-a", "one")
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
	if len(got) != 2 || got[0].Session != "bee-a" || got[0].Running || got[0].Unread != 1 ||
		got[1].Session != "queen" || !got[1].Running || got[1].Unread != 0 {
		t.Fatalf("status --json = %+v", got)
	}
}

func TestStatusRecordsNoReceipts(t *testing.T) {
	useTempLogbook(t)
	bs(t, "send", "--as", "queen", "bee-a", "hello")
	bs(t, "status")
	if out, _, _ := bs(t, "inbox", "--as", "bee-a"); !strings.Contains(out, "hello") {
		t.Fatalf("inbox after status = %q", out)
	}
}

func TestStatusWithAnEmptyLogbook(t *testing.T) {
	useTempLogbook(t)
	if out, _, code := bs(t, "status"); code != 0 || out != "no running sessions and no unread messages\n" {
		t.Fatalf("exit %d, %q", code, out)
	}
}
