package config

import (
	"errors"
	"fmt"
	"maps"
	"reflect"
	"slices"
	"strings"
)

// Template is the file bs config init writes for scope ("global" or "project"): every key
// that file may set, under a comment saying what it means, its default and its allowed
// values, commented out at its default unless set names it (by dotted key). It refuses a
// key the file can't hold and a value the loader would, so the text always loads.
func Template(scope string, set map[string]string) (string, error) {
	var cfg Config
	all := leaves(reflect.ValueOf(&cfg).Elem(), "")
	var errs []error
	for _, k := range slices.Sorted(maps.Keys(set)) {
		i := slices.IndexFunc(all, func(f leaf) bool { return f.key == k })
		switch {
		case i < 0:
			errs = append(errs, fmt.Errorf("unknown key %s", k))
		case all[i].list():
			errs = append(errs, fmt.Errorf("%s is a list: set it by hand", k))
		case !fits(all[i], scope):
			errs = append(errs, fmt.Errorf("%s belongs in the %s file, not the %s one", k, all[i].tag.Get("scope"), scope))
		default:
			errs = append(errs, check(all[i], set[k]))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return "", err
	}
	var b strings.Builder
	fmt.Fprintf(&b, "# bs's %s config, written by bs config init. A commented key keeps the default\n# shown; uncomment it to set it. bs config prints what bs reads, and from where.\n", scope)
	table := ""
	for _, f := range all {
		if !fits(f, scope) {
			continue
		}
		t, key := "", f.key
		if i := strings.LastIndex(f.key, "."); i >= 0 {
			t, key = f.key[:i], f.key[i+1:]
		}
		if t != table {
			table = t
			fmt.Fprintf(&b, "\n[%s]\n", t)
			if parts := strings.Split(t, "."); len(parts) == 2 && (parts[0] == "queen" || parts[0] == "worker") {
				fmt.Fprintf(&b, "# read only while the global harness key is %q\n", parts[1])
			}
		}
		doc := f.tag.Get("doc")
		if doc == "" {
			panic("config: " + f.key + " has no doc tag")
		}
		if enum := f.tag.Get("enum"); enum != "" {
			doc += "; one of " + strings.ReplaceAll(enum, "|", ", ")
		}
		if env := f.tag.Get("env"); env != "" {
			doc += "; env " + env + " overrides it"
		}
		b.WriteString("\n" + wrap(doc, "# ", 88))
		val := "[]"
		if !f.list() {
			val = Quote(f.tag.Get("default"))
		}
		if v, ok := set[f.key]; ok {
			fmt.Fprintf(&b, "%s = %s\n", key, Quote(v))
		} else {
			fmt.Fprintf(&b, "# %s = %s\n", key, val)
		}
	}
	text := b.String()
	// The renderer's own check: what it writes, the loader reads.
	var into Config
	if err := decodeText(reflect.ValueOf(&into).Elem(), map[string]Source{}, map[string][]string{}, File{Scope: scope, Path: "bs config init"}, text); err != nil {
		panic(err)
	}
	return text, nil
}

// fits reports whether a file of scope may set f.
func fits(f leaf, scope string) bool {
	s := f.tag.Get("scope")
	return s == "both" || s == scope
}

// wrap breaks s into lines of at most width, each starting with prefix.
func wrap(s, prefix string, width int) string {
	var b strings.Builder
	line := prefix
	for _, w := range strings.Fields(s) {
		if line != prefix && len(line)+1+len(w) > width {
			b.WriteString(line + "\n")
			line = prefix
		}
		if line != prefix {
			line += " "
		}
		line += w
	}
	return b.String() + line + "\n"
}

// Quote quotes s as a TOML basic string; Go's %q writes escapes TOML lacks (\a, \v).
// ponytail: invalid UTF-8 prints as U+FFFD, since TOML strings can't hold it.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range strings.ToValidUTF8(s, "�") {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}
