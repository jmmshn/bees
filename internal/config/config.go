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
//   - enum: its allowed values, separated by | (optional).
//
// A harness's own settings nest under its table in the role's ([worker.claude_code]);
// the role tables themselves say nothing about any harness.
type Config struct {
	Paths  Paths `toml:"paths"`
	Queen  Role  `toml:"queen"`
	Worker Role  `toml:"worker"`
}

// Paths says where bs keeps its files and finds repos.
type Paths struct {
	LogbookDir string `toml:"logbook_dir" scope:"both" env:"BS_LOGBOOK_DIR" default:"~/.local/share/bees"`
	Repos      string `toml:"repos" scope:"global" env:"BS_REPOS"`
	Repo       string `toml:"repo" scope:"project"`
	Worktrees  string `toml:"worktrees" scope:"both" default:"{repo}-WT"`
}

// Role holds the settings of the queen or of every worker.
type Role struct {
	ClaudeCode ClaudeCode `toml:"claude_code"`
}

// ClaudeCode holds the settings bs passes to Claude Code.
type ClaudeCode struct {
	Model string `toml:"model" scope:"both"`
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
	Sources map[string]Source // by dotted key, e.g. "worker.claude_code.model"
	Files   []File
}

// Field is one setting's dotted key, effective value and scope.
type Field struct {
	Key, Value, Scope string
}

// Fields lists every setting in declaration order.
func (l *Loaded) Fields() []Field {
	var out []Field
	for _, f := range leaves(reflect.ValueOf(&l.Config).Elem(), "") {
		out = append(out, Field{f.key, f.v.String(), f.tag.Get("scope")})
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
	l := &Loaded{Sources: map[string]Source{}}
	if err := load(&l.Config, l.Sources, &l.Files, dir, project); err != nil {
		return nil, err
	}
	return l, nil
}

func load(dst any, src map[string]Source, files *[]File, dir, project string) error {
	root := reflect.ValueOf(dst).Elem()
	for _, f := range leaves(root, "") {
		f.v.SetString(f.tag.Get("default"))
		src[f.key] = Source{Kind: "default"}
	}
	paths := []File{{Scope: "global", Path: filepath.Join(dir, "config.toml")}}
	var errs []error
	if project != "" {
		paths = append(paths, File{Scope: "project", Path: filepath.Join(dir, "projects", project+".toml")})
		errs = append(errs, misnamed(filepath.Join(dir, "projects"), project))
	}
	for _, file := range paths {
		ok, err := decodeFile(root, src, file)
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
		if err := checkEnum(f, val); err != nil {
			errs = append(errs, fmt.Errorf("env %s: %w", env, err))
			continue
		}
		f.v.SetString(val)
		src[f.key] = Source{Kind: "env", Where: env}
	}
	return errors.Join(errs...)
}

// decodeFile lays one file's keys over root and records them in src. It reports
// whether the file exists; a missing file sets nothing and is not an error.
func decodeFile(root reflect.Value, src map[string]Source, file File) (bool, error) {
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
		if err := checkEnum(f, f.v.String()); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", file.Path, err))
			continue
		}
		out[i].v.SetString(f.v.String())
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

func checkEnum(f leaf, val string) error {
	enum := f.tag.Get("enum")
	if enum == "" || slices.Contains(strings.Split(enum, "|"), val) {
		return nil
	}
	return fmt.Errorf("%s = %q: must be one of %s", f.key, val, strings.ReplaceAll(enum, "|", ", "))
}

// leaf is one setting found by walking a config struct.
type leaf struct {
	key string // dotted, e.g. "paths.repo"
	tag reflect.StructTag
	v   reflect.Value
}

// leaves walks v's fields in declaration order: a struct is a table, a string a setting.
// ponytail: settings are strings only; durations and lists come with the keys that need them.
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
		default:
			panic("config: " + key + " has unsupported type " + sf.Type.String())
		}
	}
	return out
}
