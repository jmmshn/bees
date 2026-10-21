// Package config reads bs's settings from $XDG_CONFIG_HOME/bees, else ~/.config/bees:
// a global config.toml plus projects/<project>.toml (ADR-0008). Config is the only
// place a setting is declared; the loader reads everything else from its struct tags.
package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	pr "github.com/jmmshn/bees/internal/project"
)

// Config is every setting bs reads. Its tables mirror the TOML tables. Each leaf is
// tagged with:
//
//   - toml: its key within its table;
//   - scope: which file may set it, global, project or both;
//   - default: its value when nothing sets it (absent means "");
//   - env: the variable that overrides both files (optional);
//   - enum: its allowed values, separated by | (optional);
//   - path: a path, checked as ADR-0008 says: "dir" or "file", or "{repo}" when the
//     value may begin with the {repo} placeholder (optional).
//
// A setting is a string or a list of strings; a list's default is empty, it has no env
// override, and enum and path check each of its items.
//
// A harness's own settings nest under its table in the role's ([worker.claude_code]),
// named by the harness's config name; the role tables themselves say nothing about any
// harness. Only the active harness's tables may be set (ADR-0009).
type Config struct {
	// Harness is the harness every new queen and worker on this machine starts on.
	Harness  string   `toml:"harness" scope:"global" default:"claude_code" enum:"claude_code|agy"`
	Paths    Paths    `toml:"paths"`
	Delivery Delivery `toml:"delivery"`
	Guards   Guards   `toml:"guards"`
	Queen    Role     `toml:"queen"`
	Worker   Worker   `toml:"worker"`
}

// Delivery says how a worker's work lands (ADR-0009): it picks the role prompts' delivery
// fragment, the guards' default and which proofs bs prune uses.
type Delivery struct {
	Kind     string `toml:"kind" scope:"both" default:"github_pr" enum:"github_pr|manual"`
	Fragment string `toml:"fragment" scope:"both" path:"file"` // the user's own wording instead of kind's
}

// Guards are the commands every worker is denied, as harness-neutral shell globs over a
// command line (* matches any text), plus scoped grants (ADR-0009, bees-12.13).
type Guards struct {
	Deny           []string `toml:"deny" scope:"both"`             // empty: the delivery kind's default
	AllowMergeInto []string `toml:"allow_merge_into" scope:"both"` // base-branch globs a worker may merge a PR into
}

// Paths says where bs keeps its files and finds repos.
type Paths struct {
	LogbookDir string `toml:"logbook_dir" scope:"both" env:"BS_LOGBOOK_DIR" default:"~/.local/share/bees" path:"dir"`
	Repos      string `toml:"repos" scope:"global" env:"BS_REPOS" path:"dir"`
	Repo       string `toml:"repo" scope:"project" path:"dir"`
	Worktrees  string `toml:"worktrees" scope:"both" default:"{repo}-WT" path:"{repo}"`
}

// Role holds the settings of the queen or of every worker.
type Role struct {
	ClaudeCode ClaudeCode `toml:"claude_code"`
	Agy        Agy        `toml:"agy"`
}

// Worker holds every worker's settings: its harness's, as in Role, and bs spawn's.
type Worker struct {
	ClaudeCode ClaudeCode `toml:"claude_code"`
	Agy        Agy        `toml:"agy"`
	// Path is bs spawn --path when the call passes none: dirs put first on the PATH of a
	// new worker's shell commands, so a live test's toy spawns the build under test
	// (bees-12.24).
	Path []string `toml:"path" scope:"project" path:"dir"`
}

// Role is w's harness tables, for the adapter, without bs spawn's own settings.
func (w Worker) Role() Role { return Role{ClaudeCode: w.ClaudeCode, Agy: w.Agy} }

// ClaudeCode holds the settings bs passes to Claude Code.
type ClaudeCode struct {
	Model string `toml:"model" scope:"both"`
}

// Agy holds the settings bs passes to Antigravity's CLI.
type Agy struct {
	Model string `toml:"model" scope:"both"` // an id from agy models; empty: agy's default
}

// Source says where a value came from.
type Source struct {
	Kind  string // "default", "global", "project" or "env"
	Where string // the file, or the variable for env; "" for default
}

func (s Source) String() string {
	if s.Where == "" {
		return s.Kind
	}
	return s.Kind + " " + s.Where
}

// File is a config file bs looked for. A missing file is fine and sets nothing.
type File struct {
	Scope  string // "global" or "project"
	Path   string
	Exists bool
}

// Loaded is the effective config: the values, each one's source, and the files read.
type Loaded struct {
	Config
	Sources  map[string]Source   // by dotted key, e.g. "worker.claude_code.model"
	Shadowed map[string][]string // by dotted key: the values lower layers set, lowest first
	Files    []File
}

// Field is one setting's dotted key, effective value and scope. A list setting has
// List set and its items in Items; Value is then "".
type Field struct {
	Key, Value, Scope string
	List              bool
	Items             []string
}

// Fields lists every setting in declaration order.
func (l *Loaded) Fields() []Field {
	var out []Field
	for _, f := range leaves(reflect.ValueOf(&l.Config).Elem(), "") {
		fd := Field{Key: f.key, Scope: f.tag.Get("scope"), List: f.list()}
		if fd.List {
			fd.Items = f.items()
		} else {
			fd.Value = f.v.String()
		}
		out = append(out, fd)
	}
	return out
}

// Dir is bs's config directory: $XDG_CONFIG_HOME/bees, else ~/.config/bees, on every
// OS (never os.UserConfigDir, which is ~/Library/Application Support on macOS).
func Dir() (string, error) {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		if !filepath.IsAbs(x) {
			return "", fmt.Errorf("XDG_CONFIG_HOME=%s is not an absolute path", x)
		}
		return filepath.Join(x, "bees"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".config", "bees"), nil
}

// Load reads the config for project (a normalised name; "" reads the global file only).
// Each value is filled default → global → project → env. Any error in a file fails the
// whole load and names the file and key path; every error found is returned at once.
func Load(project string) (*Loaded, error) {
	dir, err := Dir()
	if err != nil {
		return nil, err
	}
	return loadInto(dir, project)
}

// Env is Load with neither file read: defaults, then env. The commands that skip the
// files while BS_LOGBOOK_DIR is set load this (ADR-0008).
func Env() (*Loaded, error) { return loadInto("", "") }

func loadInto(dir, project string) (*Loaded, error) {
	l := &Loaded{Sources: map[string]Source{}, Shadowed: map[string][]string{}}
	if err := load(&l.Config, l.Sources, l.Shadowed, &l.Files, dir, project); err != nil {
		return nil, err
	}
	return l, nil
}

// load fills dst default → global → project → env; dir "" reads no file.
func load(dst any, src map[string]Source, shadowed map[string][]string, files *[]File, dir, project string) error {
	root := reflect.ValueOf(dst).Elem()
	for _, f := range leaves(root, "") {
		if f.list() {
			f.v.SetZero()
		} else {
			f.v.SetString(f.tag.Get("default"))
		}
		src[f.key] = Source{Kind: "default"}
	}
	var paths []File
	var errs []error
	if dir != "" {
		paths = append(paths, File{Scope: "global", Path: filepath.Join(dir, "config.toml")})
	}
	if dir != "" && project != "" {
		paths = append(paths, File{Scope: "project", Path: filepath.Join(dir, "projects", project+".toml")})
		errs = append(errs, misnamed(filepath.Join(dir, "projects"), project))
	}
	for _, file := range paths {
		ok, err := decodeFile(root, src, shadowed, file)
		file.Exists = ok
		*files = append(*files, file)
		errs = append(errs, err)
	}
	for _, f := range leaves(root, "") {
		env := f.tag.Get("env")
		if env == "" || os.Getenv(env) == "" {
			continue
		}
		val := os.Getenv(env)
		if err := check(f, val); err != nil {
			errs = append(errs, fmt.Errorf("env %s: %w", env, err))
			continue
		}
		shadowed[f.key] = append(shadowed[f.key], f.v.String())
		f.v.SetString(val)
		src[f.key] = Source{Kind: "env", Where: env}
	}
	// A harness off its enum already has its error; every table would be inactive under it.
	sf, _ := root.Type().FieldByName("Harness")
	if h := root.FieldByName("Harness").String(); check(leaf{key: "harness", tag: sf.Tag}, h) == nil {
		errs = append(errs, inactive(root, src, h)...)
	}
	return errors.Join(errs...)
}

// inactive refuses a value set in a role's table for a harness other than the active one:
// bs would never read it. A role's tables are named by harness config names.
func inactive(root reflect.Value, src map[string]Source, harness string) []error {
	var errs []error
	seen := map[string]bool{}
	for _, f := range leaves(root, "") {
		parts := strings.Split(f.key, ".")
		if len(parts) != 3 || parts[0] != "queen" && parts[0] != "worker" || parts[1] == harness {
			continue
		}
		s := src[f.key]
		table := parts[0] + "." + parts[1]
		if (s.Kind == "global" || s.Kind == "project") && !seen[s.Where+table] {
			seen[s.Where+table] = true
			errs = append(errs, fmt.Errorf("%s: [%s] is for harness %s, but the harness key says %s, so bs never reads it", s.Where, table, parts[1], harness))
		}
	}
	return errs
}

// decodeFile lays one file's keys over root and records them in src. It reports
// whether the file exists; a missing file sets nothing and is not an error.
func decodeFile(root reflect.Value, src map[string]Source, shadowed map[string][]string, file File) (bool, error) {
	b, err := os.ReadFile(file.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err // fail closed: an unreadable file is not a missing one
	}
	read := reflect.New(root.Type())
	md, err := toml.Decode(string(b), read.Interface())
	if err != nil {
		return true, fmt.Errorf("%s: %w", file.Path, err)
	}
	var errs []error
	var unknown []string
	for _, k := range md.Undecoded() {
		// An unknown table lists itself and then each key in it: name only the table.
		if !slices.ContainsFunc(unknown, func(t string) bool { return strings.HasPrefix(k.String(), t+".") }) {
			unknown = append(unknown, k.String())
			errs = append(errs, fmt.Errorf("%s: unknown key %s", file.Path, k))
		}
	}
	in, out := leaves(read.Elem(), ""), leaves(root, "")
	for i, f := range in {
		if !md.IsDefined(strings.Split(f.key, ".")...) {
			continue
		}
		if scope := f.tag.Get("scope"); scope != "both" && scope != file.Scope {
			errs = append(errs, fmt.Errorf("%s: %s belongs in the %s file, not the %s one", file.Path, f.key, scope, file.Scope))
			continue
		}
		if err := checkAll(f); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", file.Path, err))
			continue
		}
		shadowed[f.key] = append(shadowed[f.key], out[i].text())
		out[i].v.Set(f.v)
		src[f.key] = Source{Kind: file.Scope, Where: file.Path}
	}
	return true, errors.Join(errs...)
}

// misnamed refuses a file in dir that names project in another spelling
// (projects/jobflow-ray.toml for project jobflowray): bs would never read it.
func misnamed(dir, project string) error {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var errs []error
	for _, e := range entries {
		stem, ok := strings.CutSuffix(e.Name(), ".toml")
		if ok && stem != project && pr.Normalise(stem) == project {
			errs = append(errs, fmt.Errorf("%s: bs never reads it; rename it to %s",
				filepath.Join(dir, e.Name()), filepath.Join(dir, project+".toml")))
		}
	}
	return errors.Join(errs...)
}

// checkAll checks a string setting's value, or each item of a list setting's.
func checkAll(f leaf) error {
	if !f.list() {
		return check(f, f.v.String())
	}
	var errs []error
	for _, it := range f.items() {
		if it == "" {
			errs = append(errs, fmt.Errorf("%s: an empty item", f.key)) // unset is an empty list, not [""]
			continue
		}
		errs = append(errs, check(f, it))
	}
	return errors.Join(errs...)
}

// check refuses a value off its enum, or a path ADR-0008 doesn't allow: after Expand it
// must be absolute, and {repo} may only begin a "{repo}" path. An empty value passes only
// where it is the default, meaning unset.
func check(f leaf, val string) error {
	if enum := f.tag.Get("enum"); enum != "" && !slices.Contains(strings.Split(enum, "|"), val) {
		return fmt.Errorf("%s = %q: must be one of %s", f.key, val, strings.ReplaceAll(enum, "|", ", "))
	}
	kind := f.tag.Get("path")
	if kind == "" || val == "" && f.tag.Get("default") == "" {
		return nil
	}
	rest := val
	if after, ok := strings.CutPrefix(val, pr.RepoPlaceholder); ok && kind == pr.RepoPlaceholder {
		rest = "/" + after // {repo} is a main checkout: always absolute
	}
	_, homeErr := os.UserHomeDir()
	switch {
	case strings.Contains(rest, pr.RepoPlaceholder) && kind == pr.RepoPlaceholder:
		return fmt.Errorf("%s = %q: %s is only allowed at its start", f.key, val, pr.RepoPlaceholder)
	case strings.Contains(rest, pr.RepoPlaceholder):
		return fmt.Errorf("%s = %q: %s is only allowed at the start of a worktrees path", f.key, val, pr.RepoPlaceholder)
	case strings.HasPrefix(rest, "~") && homeErr != nil:
		return fmt.Errorf("%s = %q: can't expand ~: %w", f.key, val, homeErr)
	case !filepath.IsAbs(Expand(rest)):
		return fmt.Errorf("%s = %q: not an absolute path (only ~ is expanded)", f.key, val)
	}
	return nil
}

// Expand replaces a leading ~ (alone or before /) with the home directory: the only
// expansion bs does in a path, never $VAR or ~user.
func Expand(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return path // left relative, so check refuses it
	}
	return filepath.Join(home, path[1:])
}

// leaf is one setting found by walking a config struct.
type leaf struct {
	key string // dotted, e.g. "paths.repo"
	tag reflect.StructTag
	v   reflect.Value
}

func (f leaf) list() bool { return f.v.Kind() == reflect.Slice }

func (f leaf) items() []string { return slices.Clone(f.v.Interface().([]string)) }

// text is the value as Shadowed keeps it: a string as is, a list's items joined by ", ".
func (f leaf) text() string {
	if f.list() {
		return strings.Join(f.items(), ", ")
	}
	return f.v.String()
}

// leaves walks v's fields in declaration order: a struct is a table, a string or a list
// of strings a setting.
// ponytail: durations come with the keys that need them.
func leaves(v reflect.Value, prefix string) []leaf {
	var out []leaf
	for i := range v.NumField() {
		sf := v.Type().Field(i)
		key := prefix + sf.Tag.Get("toml")
		switch sf.Type.Kind() {
		case reflect.Struct:
			out = append(out, leaves(v.Field(i), key+".")...)
		case reflect.String:
			out = append(out, leaf{key, sf.Tag, v.Field(i)})
		case reflect.Slice:
			if sf.Type.Elem().Kind() != reflect.String || sf.Tag.Get("env") != "" || sf.Tag.Get("default") != "" {
				panic("config: " + key + ": a list holds strings, with no env or default")
			}
			out = append(out, leaf{key, sf.Tag, v.Field(i)})
		default:
			panic("config: " + key + " has unsupported type " + sf.Type.String())
		}
	}
	return out
}
