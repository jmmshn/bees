package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// bs guard denies a run_command a glob matches with agy's deny JSON, gives everything
// else no decision as empty stdout (never {}: agy reads it as a deny), and denies a call
// it can't read; it always exits 0, and needs no config or project.
func TestGuardDeniesAMatchAndOtherwiseSaysNothing(t *testing.T) {
	call := func(tool, cmd string) string {
		args := map[string]string{}
		if cmd != "" {
			args["CommandLine"] = cmd
		}
		b, _ := json.Marshal(map[string]any{"conversationId": "c", "toolCall": map[string]any{"name": tool, "args": args}})
		return string(b)
	}
	for _, c := range []struct{ stdin, want string }{
		{call("run_command", "git push origin main"), "deny"},
		{call("run_command", "gh pr merge 4"), "deny"},
		{call("run_command", "git push -u origin bees-12-12"), ""},
		{call("view_file", ""), ""},
		{"not json", "deny"},
		{"", "deny"},
		{`{"toolCall":{"name":"run_command","args":{"Command":"git push origin main"}}}`, "deny"},
		{`{"toolCall":{"args":{"CommandLine":"ls"}}}`, "deny"},
	} {
		var out, errb bytes.Buffer
		env := Env{Dir: t.TempDir(), Stdin: strings.NewReader(c.stdin)}
		code := Run(context.Background(), env, Ports{}, []string{"guard", "git push *main*", "gh pr merge*"}, &out, &errb)
		if code != 0 || errb.Len() != 0 {
			t.Fatalf("guard on %q: exit %d, stderr %q", c.stdin, code, errb.String())
		}
		if c.want == "" {
			if out.Len() != 0 {
				t.Errorf("guard on %q printed %q, want nothing", c.stdin, out.String())
			}
			continue
		}
		var got map[string]string
		if err := json.Unmarshal(out.Bytes(), &got); err != nil || got["decision"] != c.want || got["reason"] == "" {
			t.Errorf("guard on %q = %q (%v), want a %s with a reason", c.stdin, out.String(), err, c.want)
		}
	}
}

// With no stdin at all, bs guard denies rather than crash.
func TestGuardDeniesWithNoStdin(t *testing.T) {
	var out bytes.Buffer
	if code := Run(context.Background(), Env{}, Ports{}, []string{"guard"}, &out, &out); code != 0 || !strings.Contains(out.String(), `"deny"`) {
		t.Fatalf("exit %d, %q", code, out.String())
	}
}
