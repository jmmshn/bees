package config

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"github.com/jmmshn/bees/cmd/bsx/internal/bsname"
)

// examples are the commented example a unit table gets in a written file: a table's
// keys come from the struct, but its shape only shows in use.
var examples = map[string]string{
	"workspace.command": `[workspace.command]
create  = ["jj", "workspace", "add", "{dir}", "--name", "{name}"]
remove  = ["jj", "workspace", "forget", "{name}"]
clean   = ["sh", "-c", "test -z \"$(jj diff --summary)\""]
dir     = "{repo}-WS/{name}"
timeout = "60s"`,
	"goal": `[goal.cl]
handoff  = ["cmd:cl_sent"]
complete = ["cmd:cl_submitted"]
prune    = ["cmd:cl_submitted"]`,
	"checks": `[checks.cl_sent]
run     = ["cl", "sent", "{name}"]
timeout = "30s"
[checks.cl_submitted]
run     = ["cl", "submitted", "{name}"]`,
}

// Template is a config file for scope (global or project) holding every key that file may
// set, each under its doc comment: commented out at its default, unless set gives it a
// value (by dotted key, as the file would hold it). A key outside the scope, or one
// that isn't a plain setting, is an error. Load checks the values.
func Template(scope string, set map[string]string) (string, error) {
	var c Config
	all := leaves(reflect.ValueOf(&c).Elem(), "")
	for k := range set {
		i := slices.IndexFunc(all, func(f leaf) bool { return f.key == k })
		switch {
		case i < 0 || all[i].table() || all[i].v.Kind() != reflect.String:
			return "", fmt.Errorf("%s: not a setting config init writes", k)
		case !inScope(all[i], scope):
			return "", fmt.Errorf("%s belongs in the %s file, not the %s one", k, all[i].tag.Get("scope"), scope)
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# bees %s config: every key this file may hold, each commented out at its default.\n", scope)
	fmt.Fprintf(&b, "# Uncomment a line to set it; %s config prints each value and where it came from.\n", bsname.Cmd)
	section := ""
	for _, f := range all {
		if !inScope(f, scope) {
			continue
		}
		table, key := "", f.key
		if i := strings.LastIndex(f.key, "."); i >= 0 {
			table, key = f.key[:i], f.key[i+1:]
		}
		unread := ""
		if bead := f.tag.Get("unread"); bead != "" {
			unread = " (not read by " + bsname.Cmd + " yet: " + bead + ")"
		}
		if f.table() {
			fmt.Fprintf(&b, "\n# %s%s\n", f.tag.Get("doc"), unread)
			for _, l := range strings.Split(examples[f.key], "\n") {
				b.WriteString("# " + l + "\n")
			}
			section = "" // a following plain key needs its table header again
			continue
		}
		if table != section {
			fmt.Fprintf(&b, "\n[%s]\n", table)
			section = table
		}
		fmt.Fprintf(&b, "\n# %s%s\n", f.tag.Get("doc"), unread)
		if v, ok := set[f.key]; ok {
			fmt.Fprintf(&b, "%s = %s\n", key, Quote(v))
			continue
		}
		val := Quote(f.tag.Get("default"))
		if f.v.Kind() == reflect.Slice {
			val = "[]"
		} else if f.v.Kind() == reflect.Bool {
			val = "false"
		}
		fmt.Fprintf(&b, "# %s = %s\n", key, val)
	}
	return b.String(), nil
}

// inScope reports whether a file of scope may set f.
func inScope(f leaf, scope string) bool {
	s := f.tag.Get("scope")
	return s == "both" || s == scope
}
