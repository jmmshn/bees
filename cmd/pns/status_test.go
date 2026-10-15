package main

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jmmshn/peons/internal/harness/claude/claudetest"
)

func TestStatusShowsRunningAndUnreadForEachSession(t *testing.T) {
	useTempLogbook(t)
	pns(t, "send", "--as", "chief", "peon-a", "one")
	pns(t, "send", "--as", "chief", "peon-a", "two")
	pns(t, "send", "--as", "chief", "peon-b", "three")
	claudetest.FakeRegistry(t).Add(t, "peon-a", "") // chief and peon-b are not running
	out, stderr, code := pns(t, "status")
	if code != 0 {
		t.Fatalf("exit %d: %s", code, stderr)
	}
	want := "SESSION  STATE        UNREAD\n" +
		"chief    not running  0\n" +
		"peon-a   running      2\n" +
		"peon-b   not running  1\n"
	if out != want {
		t.Fatalf("status =\n%s\nwant\n%s", out, want)
	}
}

func TestStatusJSON(t *testing.T) {
	useTempLogbook(t)
	pns(t, "send", "--as", "chief", "peon-a", "one")
	claudetest.FakeRegistry(t).Add(t, "chief", "")
	out, _, code := pns(t, "status", "--json")
	var got []struct {
		Session string `json:"session"`
		Running bool   `json:"running"`
		Unread  int    `json:"unread"`
	}
	if err := json.Unmarshal([]byte(out), &got); code != 0 || err != nil {
		t.Fatalf("exit %d, %v: %q", code, err, out)
	}
	if len(got) != 2 || got[0].Session != "chief" || !got[0].Running || got[0].Unread != 0 ||
		got[1].Session != "peon-a" || got[1].Running || got[1].Unread != 1 {
		t.Fatalf("status --json = %+v", got)
	}
}

func TestStatusRecordsNoReceipts(t *testing.T) {
	useTempLogbook(t)
	pns(t, "send", "--as", "chief", "peon-a", "hello")
	pns(t, "status")
	if out, _, _ := pns(t, "inbox", "--as", "peon-a"); !strings.Contains(out, "hello") {
		t.Fatalf("inbox after status = %q", out)
	}
}

func TestStatusWithAnEmptyLogbook(t *testing.T) {
	useTempLogbook(t)
	if out, _, code := pns(t, "status"); code != 0 || out != "no sessions yet\n" {
		t.Fatalf("exit %d, %q", code, out)
	}
}
