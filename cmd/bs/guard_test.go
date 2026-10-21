package main

import (
	"encoding/json"
	"testing"
)

func TestGuardDeniesAMatchingCommandAndAsksOtherwise(t *testing.T) {
	call := func(cmd string) string {
		b, _ := json.Marshal(map[string]any{"conversationId": "c", "toolCall": map[string]any{"name": "run_command", "args": map[string]string{"CommandLine": cmd}}})
		return string(b)
	}
	for _, c := range []struct{ stdin, want string }{
		{call("git push origin main"), "deny"},
		{call("gh pr merge 4"), "deny"},
		{call("git push -u origin bees-12-12"), "ask"},
		{"not json", "deny"},
	} {
		out, stderr, code := bsIn(t, c.stdin, "guard", "git push *main*", "gh pr merge*")
		var got map[string]string
		if err := json.Unmarshal([]byte(out), &got); err != nil || code != 0 {
			t.Fatalf("guard on %q: exit %d, %q, %v, stderr %q", c.stdin, code, out, err, stderr)
		}
		if got["decision"] != c.want || (c.want == "deny" && got["reason"] == "") {
			t.Errorf("guard on %q = %v, want %s", c.stdin, got, c.want)
		}
	}
}
