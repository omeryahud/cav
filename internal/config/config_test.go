package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withConfig points XDG_CONFIG_HOME at a temp dir and, when body is non-empty,
// writes it as config.json there.
func withConfig(t *testing.T, body string) {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("XDG_CONFIG_HOME", dir)
	if body == "" {
		return
	}
	if err := os.MkdirAll(filepath.Join(dir, "cav"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cav", "config.json"), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestDirAndPathFollowXDG(t *testing.T) {
	t.Setenv("XDG_CONFIG_HOME", "/tmp/xdg")
	if got, want := Dir(), "/tmp/xdg/cav"; got != want {
		t.Errorf("Dir() = %q, want %q", got, want)
	}
	if got, want := Path(), "/tmp/xdg/cav/config.json"; got != want {
		t.Errorf("Path() = %q, want %q", got, want)
	}
}

func TestMissingFileYieldsDefaults(t *testing.T) {
	withConfig(t, "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("missing config must not error, got %v", err)
	}
	if diff := compare(cfg, Defaults()); diff != "" {
		t.Errorf("missing config should equal defaults: %s", diff)
	}
}

func TestMalformedJSONFallsBackWithError(t *testing.T) {
	withConfig(t, `{"preview": {"minWidth": 120,}}`) // trailing comma
	cfg, err := Load()
	if err == nil {
		t.Fatal("malformed JSON should report an error")
	}
	if diff := compare(cfg, Defaults()); diff != "" {
		t.Errorf("malformed config should leave defaults intact: %s", diff)
	}
}

func TestPartialOverrideLeavesOtherKeysDefault(t *testing.T) {
	withConfig(t, `{"preview": {"minWidth": 120}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Preview.MinWidth != 120 {
		t.Errorf("MinWidth = %d, want 120", cfg.Preview.MinWidth)
	}
	d := Defaults()
	if cfg.Preview.EmuCols != d.Preview.EmuCols || cfg.Preview.Refresh != d.Preview.Refresh {
		t.Error("unset preview keys should keep their defaults")
	}
	if cfg.List != d.List || cfg.Colors != d.Colors || cfg.ProjectRoot != d.ProjectRoot {
		t.Error("untouched sections should keep their defaults")
	}
}

func TestUnknownKeysIgnored(t *testing.T) {
	withConfig(t, `{"nope": 1, "preview": {"alsoNope": true, "minWidth": 111}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unknown keys should be ignored, got %v", err)
	}
	if cfg.Preview.MinWidth != 111 {
		t.Errorf("MinWidth = %d, want 111", cfg.Preview.MinWidth)
	}
}

func TestGroupingKey(t *testing.T) {
	withConfig(t, "")
	cfg, _ := Load()
	if cfg.List.Grouping != "status-dir" {
		t.Errorf("default grouping = %q, want status-dir", cfg.List.Grouping)
	}
	withConfig(t, `{"list": {"grouping": "recent"}}`)
	cfg, err := Load()
	if err != nil || cfg.List.Grouping != "recent" {
		t.Errorf("override: grouping=%q err=%v", cfg.List.Grouping, err)
	}
	withConfig(t, `{"list": {"grouping": "sideways"}}`)
	cfg, err = Load()
	if err == nil || cfg.List.Grouping != "status-dir" {
		t.Errorf("invalid value should be rejected keeping the default, got %q err=%v", cfg.List.Grouping, err)
	}
}

func TestStatusBgColors(t *testing.T) {
	withConfig(t, "")
	cfg, _ := Load()
	if cfg.Colors.RunningBg != "#0f3524" || cfg.Colors.ErrorBg != "#3a1414" {
		t.Errorf("bg defaults = %+v", cfg.Colors)
	}
	// Override one, disable another with an explicit ""; invalid value rejected.
	withConfig(t, `{"colors": {"runningBg": "#001100", "waitingBg": "", "completeBg": "nope"}}`)
	cfg, err := Load()
	if err == nil || !strings.Contains(err.Error(), "completeBg") {
		t.Fatalf("want a completeBg complaint, got %v", err)
	}
	if cfg.Colors.RunningBg != "#001100" {
		t.Errorf("RunningBg = %q", cfg.Colors.RunningBg)
	}
	if cfg.Colors.WaitingBg != "" {
		t.Errorf("explicit empty should disable the tint, got %q", cfg.Colors.WaitingBg)
	}
	if cfg.Colors.CompleteBg != Defaults().Colors.CompleteBg {
		t.Errorf("rejected value should keep the default, got %q", cfg.Colors.CompleteBg)
	}
}

func TestPreviewStartOn(t *testing.T) {
	withConfig(t, "")
	cfg, _ := Load()
	if cfg.Preview.StartOn {
		t.Error("preview should default to hidden on startup")
	}
	withConfig(t, `{"preview": {"startOn": true}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !cfg.Preview.StartOn {
		t.Error("startOn: true should be honored")
	}
	// An explicit false stays false, and siblings still apply around it.
	withConfig(t, `{"preview": {"startOn": false, "minWidth": 120}}`)
	cfg, _ = Load()
	if cfg.Preview.StartOn || cfg.Preview.MinWidth != 120 {
		t.Errorf("got StartOn=%v MinWidth=%d", cfg.Preview.StartOn, cfg.Preview.MinWidth)
	}
}

func TestIdleBackoffKeys(t *testing.T) {
	withConfig(t, "")
	cfg, _ := Load()
	if cfg.List.IdleAfter != 60*time.Second || cfg.List.IdleRefresh != 10*time.Second {
		t.Errorf("defaults = %v/%v, want 60s/10s", cfg.List.IdleAfter, cfg.List.IdleRefresh)
	}

	withConfig(t, `{"list": {"idleAfterMs": 30000, "idleRefreshMs": 5000}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.List.IdleAfter != 30*time.Second || cfg.List.IdleRefresh != 5*time.Second {
		t.Errorf("override = %v/%v, want 30s/5s", cfg.List.IdleAfter, cfg.List.IdleRefresh)
	}

	// 0 disables the backoff — a real value, not "unset".
	withConfig(t, `{"list": {"idleAfterMs": 0}}`)
	cfg, err = Load()
	if err != nil {
		t.Fatalf("idleAfterMs 0 should be accepted, got %v", err)
	}
	if cfg.List.IdleAfter != 0 {
		t.Errorf("IdleAfter = %v, want 0 (disabled)", cfg.List.IdleAfter)
	}

	// Below-minimum values are rejected and keep the default.
	withConfig(t, `{"list": {"idleAfterMs": 100, "idleRefreshMs": 10}}`)
	cfg, err = Load()
	if err == nil {
		t.Fatal("sub-minimum idle values should be reported")
	}
	if cfg.List.IdleAfter != 60*time.Second || cfg.List.IdleRefresh != 10*time.Second {
		t.Errorf("rejected values should keep defaults, got %v/%v", cfg.List.IdleAfter, cfg.List.IdleRefresh)
	}
}

func TestNewSessionDefaultsAndOverride(t *testing.T) {
	withConfig(t, "")
	cfg, _ := Load()
	if cfg.NewSession.Model != "fable" || cfg.NewSession.Effort != "max" {
		t.Errorf("defaults = %+v, want fable/max", cfg.NewSession)
	}

	withConfig(t, `{"newSession": {"model": "opus", "effort": "high"}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.NewSession.Model != "opus" || cfg.NewSession.Effort != "high" {
		t.Errorf("override = %+v, want opus/high", cfg.NewSession)
	}
}

func TestNewSessionExplicitEmptyMeansNoFlag(t *testing.T) {
	// "" is a real override here — "don't pass the flag" — not an unset key.
	withConfig(t, `{"newSession": {"model": "", "effort": ""}}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.NewSession.Model != "" || cfg.NewSession.Effort != "" {
		t.Errorf("explicit empty should clear both, got %+v", cfg.NewSession)
	}
}

func TestNewSessionBadEffortRejected(t *testing.T) {
	withConfig(t, `{"newSession": {"effort": "turbo"}}`)
	cfg, err := Load()
	if err == nil || !strings.Contains(err.Error(), "newSession.effort") {
		t.Fatalf("want a newSession.effort complaint, got %v", err)
	}
	if cfg.NewSession.Effort != "max" {
		t.Errorf("rejected effort should keep the default, got %q", cfg.NewSession.Effort)
	}
}

func TestFullOverride(t *testing.T) {
	withConfig(t, `{
	  "projectRoot": "/tmp/projects",
	  "claudeBin": "/opt/claude",
	  "preview": {"minWidth": 80, "widthPercent": 40, "emuCols": 200, "emuRows": 50,
	              "maxLogBytes": 4096, "refreshMs": 1500, "markdownStyle": "light"},
	  "list": {"nameColMax": 60, "nameColReserve": 20, "statusTTLMs": 3000, "minRefreshMs": 100},
	  "picker": {"maxDepth": 4},
	  "timeouts": {"commandMs": 10000},
	  "colors": {"running": 82, "accent": "#5fd700"}
	}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	checks := []struct {
		name      string
		got, want any
	}{
		{"ProjectRoot", cfg.ProjectRoot, "/tmp/projects"},
		{"ClaudeBin", cfg.ClaudeBin, "/opt/claude"},
		{"MinWidth", cfg.Preview.MinWidth, 80},
		{"WidthPercent", cfg.Preview.WidthPercent, 40},
		{"EmuCols", cfg.Preview.EmuCols, 200},
		{"EmuRows", cfg.Preview.EmuRows, 50},
		{"MaxLogBytes", cfg.Preview.MaxLogBytes, 4096},
		{"Refresh", cfg.Preview.Refresh, 1500 * time.Millisecond},
		{"MarkdownStyle", cfg.Preview.MarkdownStyle, "light"},
		{"NameColMax", cfg.List.NameColMax, 60},
		{"NameColReserve", cfg.List.NameColReserve, 20},
		{"StatusTTL", cfg.List.StatusTTL, 3 * time.Second},
		{"MinRefresh", cfg.List.MinRefresh, 100 * time.Millisecond},
		{"MaxDepth", cfg.Picker.MaxDepth, 4},
		{"Command", cfg.Timeouts.Command, 10 * time.Second},
		{"Colors.Running", cfg.Colors.Running, "82"},
		{"Colors.Accent", cfg.Colors.Accent, "#5fd700"},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
	// A color left unset keeps its default even when siblings are overridden.
	if cfg.Colors.Waiting != Defaults().Colors.Waiting {
		t.Errorf("Colors.Waiting = %q, want default", cfg.Colors.Waiting)
	}
}

func TestOutOfRangeValuesRejectedIndividually(t *testing.T) {
	withConfig(t, `{
	  "preview": {"minWidth": 1, "widthPercent": 99, "refreshMs": 5, "markdownStyle": "neon"},
	  "list": {"nameColMax": 0},
	  "picker": {"maxDepth": 0},
	  "timeouts": {"commandMs": 10},
	  "colors": {"running": 999, "accent": "blue", "name": 200}
	}`)
	cfg, err := Load()
	if err == nil {
		t.Fatal("out-of-range values should be reported")
	}
	d := Defaults()
	bad := []struct {
		name      string
		got, want any
	}{
		{"MinWidth", cfg.Preview.MinWidth, d.Preview.MinWidth},
		{"WidthPercent", cfg.Preview.WidthPercent, d.Preview.WidthPercent},
		{"Refresh", cfg.Preview.Refresh, d.Preview.Refresh},
		{"MarkdownStyle", cfg.Preview.MarkdownStyle, d.Preview.MarkdownStyle},
		{"NameColMax", cfg.List.NameColMax, d.List.NameColMax},
		{"MaxDepth", cfg.Picker.MaxDepth, d.Picker.MaxDepth},
		{"Command", cfg.Timeouts.Command, d.Timeouts.Command},
		{"Colors.Running", cfg.Colors.Running, d.Colors.Running},
		{"Colors.Accent", cfg.Colors.Accent, d.Colors.Accent},
	}
	for _, c := range bad {
		if c.got != c.want {
			t.Errorf("%s = %v, want default %v (value was out of range)", c.name, c.got, c.want)
		}
	}
	// A valid sibling still applies despite its neighbours being rejected.
	if cfg.Colors.Name != "200" {
		t.Errorf("Colors.Name = %q, want %q", cfg.Colors.Name, "200")
	}
	for _, want := range []string{"minWidth", "widthPercent", "markdownStyle", "colors.running", "colors.accent"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q, got %v", want, err)
		}
	}
}

func TestExplicitZeroIsRejectedNotTreatedAsUnset(t *testing.T) {
	withConfig(t, `{"preview": {"minWidth": 0}}`)
	cfg, err := Load()
	if err == nil {
		t.Fatal("an explicit 0 below the minimum should be reported, not silently ignored")
	}
	if cfg.Preview.MinWidth != Defaults().Preview.MinWidth {
		t.Errorf("MinWidth = %d, want default", cfg.Preview.MinWidth)
	}
}

func TestColorAcceptsNumberAndHexForms(t *testing.T) {
	for _, tc := range []struct{ json, want string }{
		{`{"colors": {"accent": 39}}`, "39"},
		{`{"colors": {"accent": "39"}}`, "39"},
		{`{"colors": {"accent": "#fff"}}`, "#fff"},
		{`{"colors": {"accent": "#5fd700"}}`, "#5fd700"},
		{`{"colors": {"accent": 0}}`, "0"},
		{`{"colors": {"accent": 255}}`, "255"},
	} {
		withConfig(t, tc.json)
		cfg, err := Load()
		if err != nil {
			t.Errorf("%s: unexpected error %v", tc.json, err)
		}
		if cfg.Colors.Accent != tc.want {
			t.Errorf("%s: Accent = %q, want %q", tc.json, cfg.Colors.Accent, tc.want)
		}
	}
}

func TestColorWrongJSONTypeIsReported(t *testing.T) {
	withConfig(t, `{"colors": {"accent": true}}`)
	cfg, err := Load()
	if err == nil {
		t.Fatal("a boolean colour should be reported")
	}
	if cfg.Colors.Accent != Defaults().Colors.Accent {
		t.Error("rejected colour should keep its default")
	}
}

func TestProjectRootExpansion(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Skip("no home dir")
	}
	withConfig(t, `{"projectRoot": "~/code/mine"}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := filepath.Join(home, "code", "mine"); cfg.ProjectRoot != want {
		t.Errorf("ProjectRoot = %q, want %q", cfg.ProjectRoot, want)
	}
}

func TestProjectRootEnvExpansion(t *testing.T) {
	t.Setenv("CAV_TEST_ROOT", "/tmp/envroot")
	withConfig(t, `{"projectRoot": "$CAV_TEST_ROOT/sub"}`)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "/tmp/envroot/sub"; cfg.ProjectRoot != want {
		t.Errorf("ProjectRoot = %q, want %q", cfg.ProjectRoot, want)
	}
}

func TestProjectRootMustBeAbsolute(t *testing.T) {
	for _, bad := range []string{`"relative/path"`, `""`, `"   "`} {
		withConfig(t, `{"projectRoot": `+bad+`}`)
		cfg, err := Load()
		if err == nil {
			t.Errorf("projectRoot %s should be rejected", bad)
		}
		if cfg.ProjectRoot != Defaults().ProjectRoot {
			t.Errorf("projectRoot %s: should keep the default, got %q", bad, cfg.ProjectRoot)
		}
	}
}

func TestEmptyClaudeBinIgnored(t *testing.T) {
	withConfig(t, `{"claudeBin": ""}`)
	cfg, _ := Load()
	if cfg.ClaudeBin != Defaults().ClaudeBin {
		t.Errorf("ClaudeBin = %q, want default", cfg.ClaudeBin)
	}
}

func TestValidColor(t *testing.T) {
	for _, s := range []string{"0", "255", "42", "#fff", "#FFFFFF", "#5fd700"} {
		if !validColor(s) {
			t.Errorf("validColor(%q) = false, want true", s)
		}
	}
	for _, s := range []string{"", "-1", "256", "blue", "#ff", "#gggggg", "42.5"} {
		if validColor(s) {
			t.Errorf("validColor(%q) = true, want false", s)
		}
	}
}

// compare reports the first field-level difference between two configs.
func compare(got, want Config) string {
	switch {
	case got.ProjectRoot != want.ProjectRoot:
		return "ProjectRoot " + got.ProjectRoot + " != " + want.ProjectRoot
	case got.ClaudeBin != want.ClaudeBin:
		return "ClaudeBin " + got.ClaudeBin + " != " + want.ClaudeBin
	case got.Preview != want.Preview:
		return "Preview differs"
	case got.List != want.List:
		return "List differs"
	case got.Picker != want.Picker:
		return "Picker differs"
	case got.Timeouts != want.Timeouts:
		return "Timeouts differs"
	case got.Colors != want.Colors:
		return "Colors differs"
	}
	return ""
}
