// Package config holds cav's tunable settings. Everything lives in one
// optional file, ~/.config/cav/config.json: with no file (or no key) cav
// behaves exactly as it did when these were compile-time constants, so the
// file only ever needs the handful of knobs you actually want to change.
//
// Load never fails hard — cav is a TUI, and a stray comma shouldn't lock you
// out of it. A malformed file or an out-of-range value falls back to the
// default and is reported through the returned error, which the UI surfaces
// in the footer.
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Dir is the cav config directory (XDG-aware), shared by every cav-local store.
func Dir() string {
	if x := os.Getenv("XDG_CONFIG_HOME"); x != "" {
		return filepath.Join(x, "cav")
	}
	h, _ := os.UserHomeDir()
	return filepath.Join(h, ".config", "cav")
}

// Path is the settings file itself.
func Path() string { return filepath.Join(Dir(), "config.json") }

// Config is the resolved settings: defaults with the file's overrides applied.
type Config struct {
	ProjectRoot string // where N creates new projects (and the only place it may)
	ClaudeBin   string // claude executable ($CLAUDE_BIN still wins)
	NewSession  NewSession
	Preview     Preview
	List        List
	Picker      Picker
	Timeouts    Timeouts
	Colors      Colors
}

// NewSession covers sessions cav creates (n, N, `cav -n`): the model/effort
// passed explicitly on the `claude --bg` invocation. Empty means "don't pass
// the flag" (the daemon's own default applies). Deliberately NOT inherited by
// fork/clone, which reuse the parent's respawn flags instead.
type NewSession struct {
	Model  string // --model for new sessions
	Effort string // --effort for new sessions (low|medium|high|xhigh|max)
}

// Preview covers the right-hand pane.
type Preview struct {
	MinWidth      int           // hide the pane below this terminal width
	WidthPercent  int           // pane width as a percentage of the terminal
	EmuCols       int           // emulated terminal size for a live session's screen
	EmuRows       int           // (floors: the real pane may be larger)
	MaxLogBytes   int           // cap on the trailing `claude logs` output emulated
	Refresh       time.Duration // throttle between background preview reloads
	MarkdownStyle string        // glamour style for the non-live transcript view
}

// List covers the session list and footer.
type List struct {
	NameColMax     int           // name column cap when the preview pane is on
	NameColReserve int           // columns kept for the status/age columns
	StatusTTL      time.Duration // how long a footer note lingers before clearing
	MinRefresh     time.Duration // floor between refreshes, guards a hot spin
	IdleAfter      time.Duration // no keypress for this long -> idle backoff (0 disables)
	IdleRefresh    time.Duration // poll interval while idle (any key wakes instantly)
}

// Picker covers the new-session directory picker.
type Picker struct {
	MaxDepth int // how deep below a root the walk descends
}

// Timeouts covers one-shot claude invocations (create, fork, clone).
type Timeouts struct {
	Command time.Duration
}

// Colors is the palette, by role. Values are what lipgloss accepts: an ANSI
// 256 index ("42") or a hex string ("#5fd700"). Roles are independent even
// where their defaults coincide, so retheming one never disturbs another.
type Colors struct {
	Running    string // busy dot + running bucket header
	Waiting    string // waiting-for-input dot + header
	Error      string // error dot, header, and error text
	Complete   string // complete dot + header
	Idle       string // idle dot
	IdleHeader string // idle bucket header
	Dim        string // dim/secondary text
	Help       string // the help bar
	Name       string // session names
	Accent     string // cursor, selection text, prompts

	DirHeader    string // directory header (bold name)
	DirPath      string // directory header (faint full path)
	StatusHeader string // bucket label for non-status groupings

	TitleFg     string // header bar foreground
	TitleBg     string // header bar background
	SelectionBg string // highlighted row background

	UserLabel      string // "user" role label in the preview
	AssistantLabel string // "assistant" role label in the preview
}

// Defaults returns the built-in settings — the values cav used when these were
// constants. ProjectRoot is resolved against the current home directory.
func Defaults() Config {
	home, _ := os.UserHomeDir()
	return Config{
		ProjectRoot: filepath.Join(home, "go", "src", "github.com", "omeryahud"),
		ClaudeBin:   "claude",
		NewSession:  NewSession{Model: "fable", Effort: "max"},
		Preview: Preview{
			MinWidth:      100,
			WidthPercent:  50,
			EmuCols:       220,
			EmuRows:       64,
			MaxLogBytes:   256 << 10,
			Refresh:       2 * time.Second,
			MarkdownStyle: "dark",
		},
		List: List{
			NameColMax:     50,
			NameColReserve: 18,
			StatusTTL:      6 * time.Second,
			MinRefresh:     250 * time.Millisecond,
			IdleAfter:      60 * time.Second,
			IdleRefresh:    10 * time.Second,
		},
		Picker:   Picker{MaxDepth: 8},
		Timeouts: Timeouts{Command: 25 * time.Second},
		Colors: Colors{
			Running:        "42",
			Waiting:        "214",
			Error:          "203",
			Complete:       "44",
			Idle:           "244",
			IdleHeader:     "245",
			Dim:            "244",
			Help:           "245",
			Name:           "252",
			Accent:         "39",
			DirHeader:      "147",
			DirPath:        "240",
			StatusHeader:   "242",
			TitleFg:        "254",
			TitleBg:        "238",
			SelectionBg:    "238",
			UserLabel:      "42",
			AssistantLabel: "147",
		},
	}
}

// file mirrors config.json. Every field is a pointer so an absent key is
// distinguishable from one explicitly set to a zero value.
type file struct {
	ProjectRoot *string      `json:"projectRoot"`
	ClaudeBin   *string      `json:"claudeBin"`
	NewSession  *newSessFile `json:"newSession"`
	Preview     *previewFile `json:"preview"`
	List        *listFile    `json:"list"`
	Picker      *pickerFile  `json:"picker"`
	Timeouts    *timeoutFile `json:"timeouts"`
	Colors      *colorsFile  `json:"colors"`
}

type previewFile struct {
	MinWidth      *int    `json:"minWidth"`
	WidthPercent  *int    `json:"widthPercent"`
	EmuCols       *int    `json:"emuCols"`
	EmuRows       *int    `json:"emuRows"`
	MaxLogBytes   *int    `json:"maxLogBytes"`
	RefreshMs     *int    `json:"refreshMs"`
	MarkdownStyle *string `json:"markdownStyle"`
}

type listFile struct {
	NameColMax     *int `json:"nameColMax"`
	NameColReserve *int `json:"nameColReserve"`
	StatusTTLMs    *int `json:"statusTTLMs"`
	MinRefreshMs   *int `json:"minRefreshMs"`
	IdleAfterMs    *int `json:"idleAfterMs"`
	IdleRefreshMs  *int `json:"idleRefreshMs"`
}

type pickerFile struct {
	MaxDepth *int `json:"maxDepth"`
}

type newSessFile struct {
	Model  *string `json:"model"`
	Effort *string `json:"effort"`
}

type timeoutFile struct {
	CommandMs *int `json:"commandMs"`
}

type colorsFile struct {
	Running        *colorRef `json:"running"`
	Waiting        *colorRef `json:"waiting"`
	Error          *colorRef `json:"error"`
	Complete       *colorRef `json:"complete"`
	Idle           *colorRef `json:"idle"`
	IdleHeader     *colorRef `json:"idleHeader"`
	Dim            *colorRef `json:"dim"`
	Help           *colorRef `json:"help"`
	Name           *colorRef `json:"name"`
	Accent         *colorRef `json:"accent"`
	DirHeader      *colorRef `json:"dirHeader"`
	DirPath        *colorRef `json:"dirPath"`
	StatusHeader   *colorRef `json:"statusHeader"`
	TitleFg        *colorRef `json:"titleFg"`
	TitleBg        *colorRef `json:"titleBg"`
	SelectionBg    *colorRef `json:"selectionBg"`
	UserLabel      *colorRef `json:"userLabel"`
	AssistantLabel *colorRef `json:"assistantLabel"`
}

// colorRef is a palette entry, written as either an ANSI 256 number (42) or a
// hex string ("#5fd700"). Both reach lipgloss as a string.
type colorRef struct{ s string }

func (c *colorRef) UnmarshalJSON(b []byte) error {
	var n json.Number
	if err := json.Unmarshal(b, &n); err == nil {
		c.s = n.String()
		return nil
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return fmt.Errorf("want an ANSI 256 number or a hex string, got %s", strings.TrimSpace(string(b)))
	}
	c.s = s
	return nil
}

var hexRE = regexp.MustCompile(`^#([0-9a-fA-F]{3}|[0-9a-fA-F]{6})$`)

// Load reads config.json over the defaults. The returned Config is always
// usable: a missing file yields the defaults with no error, and a malformed
// file (or a bad value) yields the defaults for whatever couldn't be applied,
// with the reasons joined into the error for the UI to display.
func Load() (Config, error) {
	cfg := Defaults()
	b, err := os.ReadFile(Path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return cfg, nil // no file: pure defaults, business as usual
		}
		return cfg, fmt.Errorf("config %s: %w (using defaults)", Path(), err)
	}
	var f file
	if err := json.Unmarshal(b, &f); err != nil {
		return cfg, fmt.Errorf("config %s: %w (using defaults)", Path(), err)
	}

	var l loader
	if f.ProjectRoot != nil {
		if p, err := expand(*f.ProjectRoot); err != nil {
			l.note("projectRoot", *f.ProjectRoot, "("+err.Error()+")")
		} else {
			cfg.ProjectRoot = p
		}
	}
	if f.ClaudeBin != nil && *f.ClaudeBin != "" {
		cfg.ClaudeBin = *f.ClaudeBin
	}
	if n := f.NewSession; n != nil {
		// An explicit "" is meaningful here: it means "don't pass the flag", so
		// the daemon's own default applies. Only the effort value is validated.
		if n.Model != nil {
			cfg.NewSession.Model = strings.TrimSpace(*n.Model)
		}
		if n.Effort != nil {
			l.setEnum(&cfg.NewSession.Effort, n.Effort, "newSession.effort",
				[]string{"", "low", "medium", "high", "xhigh", "max"}, "(want low|medium|high|xhigh|max or empty)")
		}
	}
	l.applyPreview(&cfg.Preview, f.Preview)
	l.applyList(&cfg.List, f.List)
	if p := f.Picker; p != nil {
		l.setInt(&cfg.Picker.MaxDepth, p.MaxDepth, "picker.maxDepth", 1)
	}
	if t := f.Timeouts; t != nil {
		l.setMs(&cfg.Timeouts.Command, t.CommandMs, "timeouts.commandMs", 1000)
	}
	l.applyColors(&cfg.Colors, f.Colors)

	if len(l.problems) > 0 {
		return cfg, fmt.Errorf("config: ignored %s", strings.Join(l.problems, ", "))
	}
	return cfg, nil
}

// loader applies file values onto a Config, collecting the ones it rejects.
// A rejected value never lands: the default for that key stays in place.
type loader struct{ problems []string }

func (l *loader) note(key string, val any, why string) {
	l.problems = append(l.problems, fmt.Sprintf("%s=%v %s", key, val, why))
}

// setInt applies v when it's at least min.
func (l *loader) setInt(dst *int, v *int, key string, min int) {
	switch {
	case v == nil:
	case *v < min:
		l.note(key, *v, fmt.Sprintf("(min %d)", min))
	default:
		*dst = *v
	}
}

// setRange applies v when it falls within [min, max].
func (l *loader) setRange(dst *int, v *int, key string, min, max int) {
	switch {
	case v == nil:
	case *v < min || *v > max:
		l.note(key, *v, fmt.Sprintf("(want %d-%d)", min, max))
	default:
		*dst = *v
	}
}

// setMs applies v (milliseconds) as a duration when it's at least min.
func (l *loader) setMs(dst *time.Duration, v *int, key string, min int) {
	switch {
	case v == nil:
	case *v < min:
		l.note(key, *v, fmt.Sprintf("(min %dms)", min))
	default:
		*dst = time.Duration(*v) * time.Millisecond
	}
}

// setEnum applies v when it is one of allowed.
func (l *loader) setEnum(dst *string, v *string, key string, allowed []string, why string) {
	if v == nil {
		return
	}
	for _, a := range allowed {
		if *v == a {
			*dst = *v
			return
		}
	}
	l.note(key, *v, why)
}

// setColor applies v when it parses as an ANSI 256 index or a hex string.
func (l *loader) setColor(dst *string, v *colorRef, key string) {
	switch {
	case v == nil:
	case !validColor(v.s):
		l.note("colors."+key, v.s, "(want 0-255 or #rrggbb)")
	default:
		*dst = v.s
	}
}

func (l *loader) applyPreview(dst *Preview, p *previewFile) {
	if p == nil {
		return
	}
	l.setInt(&dst.MinWidth, p.MinWidth, "preview.minWidth", 20)
	l.setInt(&dst.EmuCols, p.EmuCols, "preview.emuCols", 20)
	l.setInt(&dst.EmuRows, p.EmuRows, "preview.emuRows", 5)
	l.setInt(&dst.MaxLogBytes, p.MaxLogBytes, "preview.maxLogBytes", 1024)
	l.setMs(&dst.Refresh, p.RefreshMs, "preview.refreshMs", 100)
	l.setRange(&dst.WidthPercent, p.WidthPercent, "preview.widthPercent", 10, 90)
	l.setEnum(&dst.MarkdownStyle, p.MarkdownStyle, "preview.markdownStyle",
		[]string{"dark", "light", "notty", "ascii", "dracula", "pink", "tokyo-night"},
		"(unknown glamour style)")
}

func (l *loader) applyList(dst *List, c *listFile) {
	if c == nil {
		return
	}
	l.setInt(&dst.NameColMax, c.NameColMax, "list.nameColMax", 10)
	l.setInt(&dst.NameColReserve, c.NameColReserve, "list.nameColReserve", 8)
	l.setMs(&dst.StatusTTL, c.StatusTTLMs, "list.statusTTLMs", 500)
	l.setMs(&dst.MinRefresh, c.MinRefreshMs, "list.minRefreshMs", 50)
	// idleAfterMs: 0 is a real value — it disables the idle backoff entirely.
	if v := c.IdleAfterMs; v != nil && *v == 0 {
		dst.IdleAfter = 0
	} else {
		l.setMs(&dst.IdleAfter, c.IdleAfterMs, "list.idleAfterMs", 5000)
	}
	l.setMs(&dst.IdleRefresh, c.IdleRefreshMs, "list.idleRefreshMs", 1000)
}

func (l *loader) applyColors(dst *Colors, c *colorsFile) {
	if c == nil {
		return
	}
	for _, e := range []struct {
		dst *string
		v   *colorRef
		key string
	}{
		{&dst.Running, c.Running, "running"},
		{&dst.Waiting, c.Waiting, "waiting"},
		{&dst.Error, c.Error, "error"},
		{&dst.Complete, c.Complete, "complete"},
		{&dst.Idle, c.Idle, "idle"},
		{&dst.IdleHeader, c.IdleHeader, "idleHeader"},
		{&dst.Dim, c.Dim, "dim"},
		{&dst.Help, c.Help, "help"},
		{&dst.Name, c.Name, "name"},
		{&dst.Accent, c.Accent, "accent"},
		{&dst.DirHeader, c.DirHeader, "dirHeader"},
		{&dst.DirPath, c.DirPath, "dirPath"},
		{&dst.StatusHeader, c.StatusHeader, "statusHeader"},
		{&dst.TitleFg, c.TitleFg, "titleFg"},
		{&dst.TitleBg, c.TitleBg, "titleBg"},
		{&dst.SelectionBg, c.SelectionBg, "selectionBg"},
		{&dst.UserLabel, c.UserLabel, "userLabel"},
		{&dst.AssistantLabel, c.AssistantLabel, "assistantLabel"},
	} {
		l.setColor(e.dst, e.v, e.key)
	}
}

// validColor accepts an ANSI 256 index or a #rgb/#rrggbb hex string.
func validColor(s string) bool {
	if hexRE.MatchString(s) {
		return true
	}
	n, err := strconv.Atoi(s)
	return err == nil && n >= 0 && n <= 255
}

// expand resolves a leading ~ and any $VARs, and requires the result to be an
// absolute path — a relative projectRoot would resolve against wherever cav
// happened to be launched.
func expand(p string) (string, error) {
	p = strings.TrimSpace(p)
	if p == "" {
		return "", errors.New("empty")
	}
	if p == "~" || strings.HasPrefix(p, "~/") {
		h, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		p = filepath.Join(h, strings.TrimPrefix(strings.TrimPrefix(p, "~"), "/"))
	}
	p = os.ExpandEnv(p)
	if !filepath.IsAbs(p) {
		return "", errors.New("not an absolute path")
	}
	return filepath.Clean(p), nil
}
