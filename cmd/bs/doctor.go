package main

import (
	"bytes"
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jmmshn/bees/internal/config"
	"github.com/jmmshn/bees/internal/harness"
	"github.com/jmmshn/bees/internal/harness/agy"
	"github.com/jmmshn/bees/internal/logbook"
	"github.com/jmmshn/bees/internal/project"
	"github.com/jmmshn/bees/internal/prompt"
	"github.com/jmmshn/bees/internal/worker"
)

// semver is the first x.y.z in a --version line.
var semver = regexp.MustCompile(`\d+\.\d+\.\d+`)

// minVersion is the oldest release of each tool bs works with, by binary name.
var minVersion = map[string]string{
	"claude": "2.1.296", // cross-session messaging, which the doorbell rides on
	"bd":     "1.1.0",   // bd query
}

// doctor prints one line per check: ok, warn (bs works, with a caveat) or FAIL (a bs
// command would refuse or misbehave).
type doctor struct {
	w           io.Writer
	failed      bool
	flagProject string // --project, "" if unset
}

func (d *doctor) line(level, check, detail string) {
	if level == "FAIL" {
		d.failed = true
	}
	fmt.Fprintf(d.w, "%-6s%s: %s\n", level, check, strings.ReplaceAll(detail, "\n", "; "))
}

// cmdDoctor checks what bs needs on this machine and, in a project, what the project
// needs, one line per check. Any FAIL exits 1.
func cmdDoctor(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("doctor", flag.ContinueOnError)
	fs.SetOutput(stderr)
	proj := fs.String("project", "", "project to check too (default: BS_PROJECT, else the current git repo)")
	pos, err := parseFlags(fs, args)
	if err != nil {
		return err
	}
	if len(pos) > 0 {
		fmt.Fprintln(stderr, "usage: bs doctor [--project p]")
		return errUsage
	}
	d := &doctor{w: stdout, flagProject: *proj}
	name, projErr := project.Name(*proj)
	if projErr != nil && !errors.Is(projErr, project.ErrNoProject) {
		d.line("FAIL", "project", projErr.Error())
	}
	cfg, err := config.Load(name)
	if err != nil {
		d.line("FAIL", "config", err.Error())
		if cfg, err = config.Env(); err != nil {
			return err
		}
	} else {
		var files []string
		for _, f := range cfg.Files {
			state := ""
			if !f.Exists {
				state = " (missing: defaults)"
			}
			files = append(files, f.Scope+" "+f.Path+state)
		}
		d.line("ok", "config", strings.Join(files, ", "))
	}
	var p project.Project
	var locErr error
	if projErr == nil {
		p, locErr = locate(name, cfg, "")
	}
	d.harness(ctx, cfg, p.Repo)
	d.herdr(ctx)
	if pane := os.Getenv("HERDR_PANE_ID"); pane != "" {
		d.line("ok", "herdr pane", "this shell runs in "+pane+" (bs queen needs a herdr pane)")
	} else {
		d.line("warn", "herdr pane", "this shell is in no herdr pane (HERDR_PANE_ID is unset): start bs queen in one")
	}
	d.logbookDir(config.Expand(cfg.Paths.LogbookDir))
	d.bd(ctx)
	d.ringSockets()
	if errors.Is(projErr, project.ErrNoProject) {
		d.line("ok", "project", "none here (run in its repo, or pass --project): project checks skipped")
	} else if projErr == nil {
		d.project(ctx, p, locErr, cfg)
	}
	if d.failed {
		return exitCode(1)
	}
	return nil
}

// harness checks the configured harness's binary and version, then that its adapter
// would prepare a worker's start in dir (the project's repo; "": the home directory) with
// worker.path: agy's staged refusal shows here.
func (d *doctor) harness(ctx context.Context, cfg *config.Loaded, dir string) {
	check := "harness " + cfg.Harness
	h, err := adapter(cfg, cfg.Worker.Role())
	if err != nil {
		d.line("FAIL", check, err.Error())
		return
	}
	v, err := d.version(ctx, h.Kind())
	if err != nil {
		d.line("FAIL", check, err.Error())
		return
	}
	if dir == "" {
		if dir, err = os.UserHomeDir(); err != nil {
			d.line("FAIL", check, err.Error())
			return
		}
	}
	path := make([]string, len(cfg.Worker.Path))
	for i, p := range cfg.Worker.Path {
		path[i] = config.Expand(p)
	}
	if _, err := h.Prepare(harness.Start{Role: prompt.Worker, Name: "doctor", Dir: dir, PromptFile: os.DevNull, Path: path}); err != nil {
		d.line("FAIL", check, h.Kind()+" "+v+"; "+err.Error())
		return
	}
	d.line("ok", check, h.Kind()+" "+v)
	if cfg.Harness == agy.Name {
		d.agyTrust()
	}
}

// agyTrust checks that bs can read agy's trusted list, which agy's Prepare reads.
func (d *doctor) agyTrust() {
	path, err := agy.TrustFile()
	if err != nil {
		d.line("FAIL", "agy trusted list", err.Error())
		return
	}
	if _, err := agy.Trusted("/"); err != nil {
		d.line("FAIL", "agy trusted list", err.Error())
	} else if _, err := os.Stat(path); err != nil {
		d.line("warn", "agy trusted list", path+" is missing: agy trusts no folder yet")
	} else {
		d.line("ok", "agy trusted list", path+" is readable")
	}
}

// herdr checks herdr's version and that its server is running.
func (d *doctor) herdr(ctx context.Context) {
	v, err := d.version(ctx, "herdr")
	if err != nil {
		d.line("FAIL", "herdr", err.Error())
		return
	}
	out, err := output(ctx, "herdr", "status")
	if err != nil {
		d.line("FAIL", "herdr", fmt.Sprintf("herdr status: %v: %s", err, strings.TrimSpace(out)))
		return
	}
	_, server, ok := strings.Cut(out, "\nserver:")
	field := func(k string) string {
		if m := regexp.MustCompile(`(?m)^\s*` + k + `:\s*(.+)$`).FindStringSubmatch(server); m != nil {
			return strings.TrimSpace(m[1])
		}
		return ""
	}
	switch st := field("status"); {
	case !ok || st == "":
		d.line("FAIL", "herdr", "herdr status printed no server status: "+strings.TrimSpace(out))
	case st != "running":
		d.line("FAIL", "herdr", "server "+st+": start herdr in a terminal and run bs inside it")
	case field("endpoint_compatible") == "no":
		d.line("FAIL", "herdr", fmt.Sprintf("client %s can't talk to server %s: restart the herdr server", v, field("version")))
	case semver.MatchString(field("version")) && older(semver.FindString(field("version")), v):
		// bs's herdr probes ran against the client's version; an old server can disagree (bees-12.44).
		d.line("warn", "herdr", fmt.Sprintf("%s, but server %s is older, still running the binary it started from: restart the herdr server", v, field("version")))
	default:
		d.line("ok", "herdr", fmt.Sprintf("%s, server %s running", v, field("version")))
	}
}

// logbookDir checks that bs can make logbooks in dir.
func (d *doctor) logbookDir(dir string) {
	st, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		d.line("ok", "logbook dir", dir+" is missing: the first bs queen makes it")
	case err != nil:
		d.line("FAIL", "logbook dir", err.Error())
	case !st.IsDir():
		d.line("FAIL", "logbook dir", dir+" is not a directory")
	case syscall.Access(dir, 2) != nil: // W_OK
		d.line("FAIL", "logbook dir", dir+" is not writable")
	default:
		dbs, _ := filepath.Glob(filepath.Join(dir, "*.db"))
		d.line("ok", "logbook dir", fmt.Sprintf("%s, %d logbooks", dir, len(dbs)))
	}
}

// bd checks bd's version and that it finds a store from here.
func (d *doctor) bd(ctx context.Context) {
	v, err := d.version(ctx, "bd")
	if err != nil {
		d.line("FAIL", "bd", err.Error())
		return
	}
	out, err := output(ctx, "bd", "where")
	if err != nil {
		d.line("FAIL", "bd", v+"; bd where finds no store: "+strings.TrimSpace(out))
		return
	}
	store, _, _ := strings.Cut(strings.TrimSpace(out), "\n")
	d.line("ok", "bd", v+", store "+store)
}

// ringSockets checks the modes of bs's own ring sockets (ADR-0009 T3): the directory
// 0700, and each socket and token file in it 0600.
func (d *doctor) ringSockets() {
	run := os.Getenv("XDG_RUNTIME_DIR")
	if run == "" {
		d.line("ok", "ring sockets", "XDG_RUNTIME_DIR is unset, so bs has none")
		return
	}
	dir := filepath.Join(run, "bees")
	st, err := os.Stat(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		d.line("ok", "ring sockets", "none in "+dir+" (claude_code rings through Claude's own)")
		return
	case err != nil:
		d.line("FAIL", "ring sockets", err.Error())
		return
	case st.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()):
		d.line("FAIL", "ring sockets", dir+" belongs to another user")
		return
	case st.Mode().Perm() != 0o700:
		d.line("FAIL", "ring sockets", fmt.Sprintf("%s is mode %04o, not 0700", dir, st.Mode().Perm()))
		return
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		d.line("FAIL", "ring sockets", err.Error())
		return
	}
	for _, e := range entries {
		info, err := e.Info()
		if err != nil {
			d.line("FAIL", "ring sockets", err.Error())
			return
		}
		if info.Mode().Perm() != 0o600 {
			d.line("FAIL", "ring sockets", fmt.Sprintf("%s is mode %04o, not 0600", filepath.Join(dir, e.Name()), info.Mode().Perm()))
			return
		}
	}
	d.line("ok", "ring sockets", fmt.Sprintf("%s is 0700, its %d files 0600", dir, len(entries)))
}

// project checks what bs queen and bs spawn need of project p, found with err: its repo,
// a workspace and delivery that work together, a readable fragment, guards that build, and
// the tools its workspace and delivery kinds use.
func (d *doctor) project(ctx context.Context, p project.Project, err error, cfg *config.Loaded) {
	name := p.Name
	if err != nil {
		d.line("FAIL", "project", err.Error())
		return
	}
	switch _, err := os.Stat(p.Logbook); {
	case errors.Is(err, os.ErrNotExist):
		d.line("ok", "project "+name, "no logbook yet: bs queen makes "+p.Logbook)
	case err != nil:
		d.line("FAIL", "project "+name, err.Error())
	default:
		d.line("ok", "project "+name, "logbook "+p.Logbook)
		d.harnessSwitch(ctx, p, cfg.Harness)
	}
	none := cfg.Workspace.Kind == worker.NoWorkspace
	switch {
	case p.RepoErr() != nil:
		d.line("FAIL", "repo", p.RepoErr().Error())
	case none && !p.RepoNamed():
		d.line("FAIL", "repo", `workspace.kind = "none" shares paths.repo, which is unset: make the directory and set paths.repo in the project file`)
	case p.Repo == "":
		why := "none found: run in the project's repo, or set paths.repo in its project file"
		if h := repoHint(ctx, p, d.flagProject); h != "" {
			why = "none found for project " + p.Name + "; " + h
		}
		d.line("FAIL", "repo", why)
	default:
		if _, err := os.Stat(p.Repo); err != nil {
			d.line("FAIL", "repo", err.Error())
		} else {
			d.line("ok", "repo", fmt.Sprintf("%s (%s), workspace %s, delivery %s", p.Repo, p.RepoFrom, cfg.Workspace.Kind, cfg.Delivery.Kind))
		}
	}
	if err := worker.CheckWorkspace(cfg.Workspace.Kind, cfg.Delivery); err != nil {
		d.line("FAIL", "workspace", err.Error())
	}
	if _, err := worker.Delivery(cfg.Delivery, cfg.Guards); err != nil {
		d.line("FAIL", "delivery", err.Error())
	}
	if p.RepoErr() == nil && p.Repo != "" {
		if r, err := workerGuards(ctx, p, cfg); err != nil {
			d.line("FAIL", "guards", err.Error())
		} else {
			d.line("ok", "guards", fmt.Sprintf("%d deny rules (bs config lists them)", len(r.Deny)))
		}
	}
	if !none {
		if v, err := d.version(ctx, "git"); err != nil {
			d.line("FAIL", "git", err.Error())
		} else {
			d.line("ok", "git", v)
		}
	}
	if cfg.Delivery.Kind == "github_pr" && !none && p.RepoErr() == nil && p.Repo != "" {
		// Nothing else notices until a worker's first push (bees-12.44).
		if out, err := output(ctx, "git", "-C", p.Repo, "remote"); err == nil && strings.TrimSpace(out) == "" {
			d.line("warn", "git remote", "none in "+p.Repo+": under delivery github_pr a worker can't push its branch or open a PR; add one (git remote add origin <url>), or set delivery.kind = \"manual\"")
		}
	}
	if cfg.Delivery.Kind == "github_pr" {
		if v, err := d.version(ctx, "gh"); err != nil {
			d.line("warn", "gh", err.Error()+": bs prune and merge grants ask gh")
		} else if _, err := output(ctx, "gh", "auth", "status"); err != nil {
			d.line("warn", "gh", v+", but not logged in: run gh auth login")
		} else {
			d.line("ok", "gh", v+", logged in")
		}
	}
}

// harnessSwitch fails while a session of p runs under another harness than want: bs queen
// and bs spawn refuse to start beside it (bees-12.15).
func (d *doctor) harnessSwitch(ctx context.Context, p project.Project, want string) {
	lb, err := logbook.OpenReadOnly(ctx, p.Logbook)
	if err != nil {
		d.line("FAIL", "harness switch", err.Error())
		return
	}
	defer lb.Close()
	switch others, err := switched(ctx, lb, p.Queen(), want); {
	case err != nil:
		d.line("FAIL", "harness switch", err.Error())
	case len(others) > 0:
		verb, them := "still runs", "it"
		if len(others) > 1 {
			verb, them = "still run", "them"
		}
		d.line("FAIL", "harness switch", strings.Join(others, ", ")+" "+verb+", so bs queen and bs spawn refuse to start "+want+"; bs end "+them+" (exit a queen) first")
	}
}

// version runs bin --version and returns the first x.y.z in its output, refusing one
// older than minVersion.
func (d *doctor) version(ctx context.Context, bin string) (string, error) {
	out, err := output(ctx, bin, "--version")
	if errors.Is(err, exec.ErrNotFound) {
		return "", fmt.Errorf("%s is not on PATH", bin)
	}
	if err != nil {
		return "", fmt.Errorf("%s --version: %v: %s", bin, err, strings.TrimSpace(out))
	}
	v := semver.FindString(out)
	if v == "" {
		return "", fmt.Errorf("%s --version printed no version: %s", bin, strings.TrimSpace(out))
	}
	if min, ok := minVersion[bin]; ok && older(v, min) {
		return "", fmt.Errorf("%s %s is older than %s", bin, v, min)
	}
	return v, nil
}

// output runs bin with args, bounded to 5 s, and returns its stdout and stderr.
func output(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var b bytes.Buffer
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Stdout, cmd.Stderr, cmd.WaitDelay = &b, &b, time.Second
	err := cmd.Run()
	if ctx.Err() == context.DeadlineExceeded {
		err = fmt.Errorf("timed out after 5 s")
	}
	return b.String(), err
}

// older reports whether version a (x.y.z) is before b.
func older(a, b string) bool {
	as, bs := strings.Split(a, "."), strings.Split(b, ".")
	for i := range 3 {
		x, _ := strconv.Atoi(as[i])
		y, _ := strconv.Atoi(bs[i])
		if x != y {
			return x < y
		}
	}
	return false
}
