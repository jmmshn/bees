// Package prompt is the prompt bs appends to the sessions it starts (ADR-0010): the role
// prompt (QUEEN.md or WORKER.md), PITFALLS.md and BEADS.md, then the workspace fragment
// of the workspace kind (workspace/<kind>.md) and a goal fragment per goal type
// (goal/<type>.md). The role prompts name no VCS: what to commit and how work lands lives
// only in the fragments, so a worker gets git wording only from a git workspace or a git
// goal (bees-12.29).
package prompt

import (
	"crypto/sha256"
	"embed"
	"fmt"
	"slices"
	"strings"

	"github.com/jmmshn/bees/internal/bsname"
)

// Role picks a role prompt.
type Role string

const (
	Queen  Role = "queen"
	Worker Role = "worker"
)

var (
	//go:embed QUEEN.md
	queen string
	//go:embed WORKER.md
	worker string
	//go:embed PITFALLS.md
	pitfalls string
	//go:embed BEADS.md
	beads string
	//go:embed workspace/*.md goal/*.md host/*.md
	fragments embed.FS
)

// Hash is the sha256 of goal type typ's fragment, hex, or "" when it has none: a spawn
// freezes it with its goal (ADR-0010).
func Hash(typ string) string {
	b, err := fragments.ReadFile("goal/" + typ + ".md")
	if err != nil {
		return ""
	}
	return fmt.Sprintf("%x", sha256.Sum256(b))
}

// Text is r's prompt for project: the role prompt, PITFALLS.md and BEADS.md, then r's part
// of the workspace fragment of kind under "## Workspace" and of each goal type's fragment
// under "## Goal" (a worker: its own goal's types) or "## Goals" (the queen), with
// {{project}} and {{bs}} (the command, bsname.Cmd) filled in. A goal type with no fragment yet gets a line saying bs done
// checks it. A kind with no fragment yet (none, command: bees-13.10, 13.11) gets no
// workspace section; "" is no workspace known.
func Text(r Role, project, kind string, goals []string) string {
	role, heading := worker, "## Goal"
	if r == Queen {
		role, heading = queen, "## Goals"
	}
	var b strings.Builder
	b.WriteString(role + pitfalls + "\n" + beads)
	if ws, err := fragments.ReadFile("workspace/" + kind + ".md"); kind != "" && err == nil {
		b.WriteString("\n## Workspace\n\n" + part(r, string(ws)))
	}
	b.WriteString(goalSection(r, heading, goals))
	return fill(b.String(), project)
}

// fill puts the project and the command's name into a prompt's placeholders.
func fill(s, project string) string {
	return strings.NewReplacer("{{project}}", project, "{{bs}}", bsname.Cmd).Replace(s)
}

// Goal is r's part of each goal type's fragment under "## Goal": what bs goal sends a
// worker whose goal it amended, so the fragments it works to are the ones recorded.
func Goal(r Role, goals []string) string { return fill(goalSection(r, "## Goal", goals), "") }

func goalSection(r Role, heading string, goals []string) string {
	var b strings.Builder
	if len(goals) > 0 {
		b.WriteString("\n" + heading + "\n")
	}
	var seen []string
	for _, g := range goals {
		if slices.Contains(seen, g) {
			continue
		}
		seen = append(seen, g)
		f, err := fragments.ReadFile("goal/" + g + ".md")
		if err != nil {
			fmt.Fprintf(&b, "\n`--goal %s`: `{{bs}} done` checks it and says what is missing.\n", g)
			continue
		}
		b.WriteString("\n" + part(r, string(f)))
	}
	return b.String()
}

// part is r's share of a fragment: the shared lead, then r's own sections. A line
// "<!-- queen -->" or "<!-- worker -->" starts a part only that role reads, up to the
// next such line.
func part(r Role, fragment string) string {
	var b strings.Builder
	mine := true
	for line := range strings.SplitAfterSeq(fragment, "\n") {
		switch strings.TrimSpace(line) {
		case "<!-- queen -->":
			mine = r == Queen
		case "<!-- worker -->":
			mine = r == Worker
		default:
			if mine {
				b.WriteString(line)
			}
		}
	}
	return b.String()
}

// Host is r's part of the review host's fragment (host/<host>.md: how a change is
// uploaded, reviewed and merged there) under "## Review host", or "" for a host with
// none. The goal fragments name no host: a CL host brings its own commands here.
func Host(r Role, host string) string {
	f, err := fragments.ReadFile("host/" + host + ".md")
	if err != nil {
		return ""
	}
	return fill("\n## Review host\n\n"+part(r, string(f)), "")
}
