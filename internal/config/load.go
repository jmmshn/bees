package config

import (
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/jmmshn/bees/internal/bsname"
)

// DefaultSession is the herdr session a pane without HERDR_SESSION is on.
const DefaultSession = "default"

// RepoPlaceholder, at the start of paths.worktrees, is the main checkout.
const RepoPlaceholder = "{repo}"

// Dir is the config dir of a caller whose env getenv reads (ADR-0010 live rig):
// <base>/bees, base being XDG_CONFIG_HOME, else ~/.config, on every OS; a pane on a herdr
// session other than default reads <base>/bees/sessions/<session> instead. It returns
// the session too.
func Dir(getenv func(string) string) (dir, session string, err error) {
	base := getenv("XDG_CONFIG_HOME")
	switch home := getenv("HOME"); {
	case base != "" && !filepath.IsAbs(base):
		return "", "", fmt.Errorf("XDG_CONFIG_HOME=%s is not an absolute path", base)
	case base == "" && home == "":
		return "", "", errors.New("neither XDG_CONFIG_HOME nor HOME is set")
	case base == "":
		base = filepath.Join(home, ".config")
	}
	session = getenv("HERDR_SESSION")
	if session == "" || session == DefaultSession {
		return filepath.Join(base, "bees"), DefaultSession, nil
	}
	if strings.ContainsAny(session, "/\\\x00") || session == "." || session == ".." {
		return "", "", fmt.Errorf("HERDR_SESSION %q can't name a config dir", session)
	}
	return filepath.Join(base, "bees", "sessions", session), session, nil
}

// Input is what Load reads.
type Input struct {
	Dir     string            // the config dir
	Project string            // a normalised project name; "" reads the global file only
	Home    string            // what ~ expands to
	Env     map[string]string // the env overrides that apply: none inside a herdr pane
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
	Scope, Path string
	Exists      bool
}

// Loaded is the effective config: the values, each one's source, and the files read.
type Loaded struct {
	Config
	Sources  map[string]Source   // by dotted key; a table by its own key (goal.<name>)
	Shadowed map[string][]string // by dotted key: the values lower layers set, lowest first
	Files    []File
	home     string
}

// Load reads in's files over the defaults, default → global → project → env. Any error
// in a file fails the whole load, naming the file and key; every error found is returned
// at once.
func Load(in Input) (*Loaded, error) {
	l := Defaults(in.Home)
	root := reflect.ValueOf(&l.Config).Elem()
	files := []File{{Scope: "global", Path: filepath.Join(in.Dir, "config.toml")}}
	var errs []error
	if in.Project != "" {
		files = append(files, File{Scope: "project", Path: filepath.Join(in.Dir, "projects", in.Project+".toml")})
		errs = append(errs, misnamed(filepath.Join(in.Dir, "projects"), in.Project))
	}
	for _, file := range files {
		ok, err := l.decodeFile(root, file)
		file.Exists = ok
		l.Files = append(l.Files, file)
		errs = append(errs, err)
	}
	for _, f := range leaves(root, "") {
		name := f.tag.Get("env")
		val := in.Env[name]
		if name == "" || val == "" {
			continue
		}
		if err := l.check(f, val); err != nil {
			errs = append(errs, fmt.Errorf("env %s: %w", name, err))
			continue
		}
		l.shadow(f.key, f.v)
		f.v.SetString(val)
		l.Sources[f.key] = Source{Kind: "env", Where: name}
	}
	for _, name := range slices.Sorted(maps.Keys(l.Goal)) {
		g := l.Goal[name]
		err := g.check(name, l.Checks)
		if g.Fragment != "" {
			err = errors.Join(err, l.checkPath("fragment", g.Fragment, "dir"))
		}
		if err != nil {
			errs = append(errs, fmt.Errorf("%s: [goal.%s] %w", l.Sources["goal."+name].Where, name, err))
		}
	}
	if l.Review.Host == CommandHost {
		if err := l.Review.Command.Incomplete(); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", l.Sources["review.host"].Where, err))
		}
	}
	return l, errors.Join(errs...)
}

// Defaults is the config no file sets: what bs doctor checks against when the files
// can't be read.
func Defaults(home string) *Loaded {
	l := &Loaded{Sources: map[string]Source{}, Shadowed: map[string][]string{}, home: home}
	for _, f := range leaves(reflect.ValueOf(&l.Config).Elem(), "") {
		f.v.SetZero()
		if d := f.tag.Get("default"); d != "" && f.v.Kind() == reflect.Slice {
			f.v.Set(reflect.ValueOf([]string{d})) // a list's default is one item
		} else if d != "" {
			f.v.SetString(d)
		}
		if !f.table() {
			l.Sources[f.key] = Source{Kind: "default"}
		}
	}
	return l
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
		if ok && stem != project && Normalise(stem) == project {
			errs = append(errs, fmt.Errorf("%s: bs never reads it; rename it to %s",
				filepath.Join(dir, e.Name()), filepath.Join(dir, project+".toml")))
		}
	}
	return errors.Join(errs...)
}

// Normalise is a repo's project name: lowercased, - and _ dropped (R27⁶).
func Normalise(repo string) string {
	return strings.NewReplacer("-", "", "_", "").Replace(strings.ToLower(repo))
}

// decodeFile lays one file's keys over root. A missing file sets nothing; an unreadable
// one is an error (fail closed).
func (l *Loaded) decodeFile(root reflect.Value, file File) (bool, error) {
	b, err := os.ReadFile(file.Path)
	if errors.Is(err, fs.ErrNotExist) {
		return false, nil
	}
	if err != nil {
		return true, err
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
		src := Source{Kind: file.Scope, Where: file.Path}
		if err := l.set(f, out[i], src); err != nil {
			errs = append(errs, fmt.Errorf("%s: %w", file.Path, err))
		}
	}
	return true, errors.Join(errs...)
}

// set checks f, read from a file, and lays it over dst: a map entry by entry, anything
// else whole.
func (l *Loaded) set(f, dst leaf, src Source) error {
	switch {
	case f.v.Kind() == reflect.Map:
		if dst.v.IsNil() {
			dst.v.Set(reflect.MakeMap(f.v.Type()))
		}
		var errs []error
		iter := f.v.MapRange()
		for iter.Next() {
			key := f.key + "." + iter.Key().String()
			if c, ok := iter.Value().Interface().(interface{ check() error }); ok {
				if err := c.check(); err != nil {
					errs = append(errs, fmt.Errorf("[%s] %w", key, err))
					continue
				}
			}
			dst.v.SetMapIndex(iter.Key(), iter.Value())
			l.Sources[key] = src
		}
		return errors.Join(errs...)
	case f.v.Kind() == reflect.Pointer:
		if err := f.v.Interface().(interface{ check() error }).check(); err != nil {
			return fmt.Errorf("[%s] %w", f.key, err)
		}
	case f.v.Kind() == reflect.Slice:
		var errs []error
		for _, it := range f.v.Interface().([]string) {
			if it == "" {
				errs = append(errs, fmt.Errorf("%s: an empty item", f.key)) // unset is [], not [""]
				continue
			}
			errs = append(errs, l.check(f, it))
		}
		if err := errors.Join(errs...); err != nil {
			return err
		}
	case f.v.Kind() == reflect.String:
		if err := l.check(f, f.v.String()); err != nil {
			return err
		}
	}
	l.shadow(f.key, dst.v)
	dst.v.Set(f.v)
	l.Sources[f.key] = src
	return nil
}

// shadow records v, a string setting's value about to be overridden, under key, so bs
// can tell a moved paths.logbook_dir from a new one.
func (l *Loaded) shadow(key string, v reflect.Value) {
	if v.Kind() == reflect.String {
		l.Shadowed[key] = append(l.Shadowed[key], v.String())
	}
}

// check refuses a value off its enum, or a path ADR-0008 doesn't allow: after Expand it
// must be absolute, and {repo} may only begin a "{repo}" path. An empty value passes only
// where it is the default, meaning unset.
func (l *Loaded) check(f leaf, val string) error {
	if val == "" && f.tag.Get("default") == "" {
		return nil
	}
	if enum := f.tag.Get("enum"); enum != "" && !slices.Contains(strings.Split(enum, "|"), val) {
		return fmt.Errorf("%s = %q: must be one of %s", f.key, val, strings.ReplaceAll(enum, "|", ", "))
	}
	if _, ok := f.tag.Lookup("glob"); ok {
		if _, err := path.Match(val, ""); err != nil || strings.HasPrefix(val, "/") || strings.Contains(val, "//") {
			return fmt.Errorf("%s = %q: not a path pattern relative to the worktree (path.Match syntax, no leading /)", f.key, val)
		}
	}
	if kind := f.tag.Get("path"); kind != "" {
		return l.checkPath(f.key, val, kind)
	}
	if name := f.tag.Get("program"); name != "" {
		return l.checkProgram(f.key, val, name)
	}
	return nil
}

// checkProgram checks a program value: name itself, found on PATH, or an absolute path
// (~ expanded) whose base name is name, the program herdr's --kind starts.
func (l *Loaded) checkProgram(key, val, name string) error {
	switch {
	case filepath.Base(val) != name:
		return fmt.Errorf("%s = %q: its base name must be %s: herdr's --kind %s starts only a program of that name (bees-14.7); point a symlink named %s at it", key, val, name, name, name)
	case val == name:
		return nil
	}
	return l.checkPath(key, val, "dir")
}

// checkPath checks a path value of kind "dir", or "{repo}" when it may begin with the
// {repo} placeholder: absolute once ~ is expanded.
func (l *Loaded) checkPath(key, val, kind string) error {
	rest := val
	if after, ok := strings.CutPrefix(val, RepoPlaceholder); ok && kind == RepoPlaceholder {
		rest = "/" + after // {repo} is a main checkout: always absolute
	}
	switch {
	case strings.Contains(rest, RepoPlaceholder):
		return fmt.Errorf("%s = %q: %s is only allowed at the start of paths.worktrees", key, val, RepoPlaceholder)
	case strings.HasPrefix(rest, "~") && l.home == "":
		return fmt.Errorf("%s = %q: can't expand ~: HOME is unset", key, val)
	case !filepath.IsAbs(l.Expand(rest)):
		return fmt.Errorf("%s = %q: not an absolute path (only ~ is expanded)", key, val)
	}
	return nil
}

// Expand replaces a leading ~ (alone or before /) with the home directory: the only
// expansion bs does in a path, never $VAR or ~user.
func (l *Loaded) Expand(path string) string {
	if path != "~" && !strings.HasPrefix(path, "~/") || l.home == "" {
		return path
	}
	return filepath.Join(l.home, path[1:])
}

// Field is one setting as bs config prints it: its dotted key, its value as TOML, its
// scope and its source. A table set in a file prints one Field per key in it.
type Field struct {
	Key, Value, Scope string
	Source            Source
	Unread            string // "" when bs acts on the key, else why it doesn't yet
	Unset             bool   // a table no file sets: Key is its name, as [goal.<name>] for a map
}

// Fields lists every setting in declaration order, map entries by name.
func (l *Loaded) Fields() []Field {
	var out []Field
	for _, f := range leaves(reflect.ValueOf(&l.Config).Elem(), "") {
		scope, unread := f.tag.Get("scope"), ""
		if bead, ok := f.tag.Lookup("unread"); ok {
			unread = "not read by " + bsname.Cmd + " yet"
			if bead != "" {
				unread += " (" + bead + ")"
			}
		}
		switch f.v.Kind() {
		case reflect.Map:
			keys := f.v.MapKeys()
			if len(keys) == 0 {
				out = append(out, Field{Key: f.key + ".<name>", Scope: scope, Unread: unread, Unset: true})
			}
			slices.SortFunc(keys, func(a, b reflect.Value) int { return strings.Compare(a.String(), b.String()) })
			for _, k := range keys {
				key := f.key + "." + k.String()
				out = append(out, tableFields(key, f.v.MapIndex(k), scope, unread, l.Sources[key])...)
			}
		case reflect.Pointer:
			if f.v.IsNil() {
				out = append(out, Field{Key: f.key, Scope: scope, Unread: unread, Unset: true})
			} else {
				out = append(out, tableFields(f.key, f.v.Elem(), scope, unread, l.Sources[f.key])...)
			}
		default:
			out = append(out, Field{Key: f.key, Value: render(f.v), Scope: scope, Source: l.Sources[f.key], Unread: unread})
		}
	}
	return out
}

func tableFields(key string, v reflect.Value, scope, unread string, src Source) []Field {
	var out []Field
	for i := range v.NumField() {
		out = append(out, Field{Key: key + "." + v.Type().Field(i).Tag.Get("toml"), Value: render(v.Field(i)), Scope: scope, Source: src, Unread: unread})
	}
	return out
}

// render is v as a TOML value.
func render(v reflect.Value) string {
	switch v.Kind() {
	case reflect.Bool:
		return strconv.FormatBool(v.Bool())
	case reflect.Slice:
		q := make([]string, v.Len())
		for i := range q {
			q[i] = Quote(v.Index(i).String())
		}
		return "[" + strings.Join(q, ", ") + "]"
	}
	return Quote(v.String())
}

// Quote is s as a TOML basic string.
func Quote(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"' || r == '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(&b, "\\u%04X", r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// leaf is one setting or table found by walking a config struct.
type leaf struct {
	key string // dotted, e.g. "paths.repo"
	tag reflect.StructTag
	v   reflect.Value
}

func (f leaf) table() bool { return f.v.Kind() == reflect.Map || f.v.Kind() == reflect.Pointer }

// checker is a unit table that checks itself.
var checker = reflect.TypeFor[interface{ check() error }]()

// leaves walks v's fields in declaration order: a plain struct is a table of settings;
// a string, bool or list of strings is a setting; a pointer or map is a unit table.
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
		case reflect.Bool:
			if sf.Tag.Get("env") != "" || sf.Tag.Get("default") != "" || sf.Tag.Get("path") != "" {
				panic("config: " + key + ": a bool takes no env, default or path")
			}
			out = append(out, leaf{key, sf.Tag, v.Field(i)})
		case reflect.Pointer, reflect.Map:
			if sf.Type.Kind() == reflect.Pointer && !sf.Type.Implements(checker) {
				panic("config: " + key + ": a pointer table needs a check method")
			}
			out = append(out, leaf{key, sf.Tag, v.Field(i)})
		case reflect.Slice:
			if sf.Type.Elem().Kind() != reflect.String || sf.Tag.Get("env") != "" {
				panic("config: " + key + ": a list holds strings, with no env (a default is one item)")
			}
			out = append(out, leaf{key, sf.Tag, v.Field(i)})
		default:
			panic("config: " + key + " has unsupported type " + sf.Type.String())
		}
	}
	return out
}
