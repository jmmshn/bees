package claude

import "github.com/jmmshn/bees/internal/guards"

// Permissions renders r as Claude Code settings: permissions.deny rules, which run before
// the auto mode classifier, and the autoMode.allow list. A Bash rule's * matches any text,
// a space or a slash too (probed on Claude Code 2.1.302), so a guard's glob is its rule as
// written. allow keeps "$defaults": a list without it replaces the built-in rules (force
// push, exfiltration).
func Permissions(r guards.Rules) (deny, allow []string) {
	for _, g := range r.Deny {
		deny = append(deny, "Bash("+g+")")
	}
	return deny, append([]string{"$defaults"}, r.Allow...)
}
