package claude

import (
	"errors"
	"fmt"
	"math/rand"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// withAttachProcs stubs the process list, one output per scan (the last one
// repeats), in an empty home. It returns the home and the scan counter.
func withAttachProcs(t *testing.T, err error, outs ...string) (home string, scans *int) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	origProcs, origSettle := attachProcs, agentsViewSettle
	agentsViewSettle = 0
	knownViews.pids = nil
	scans = new(int)
	attachProcs = func() ([]byte, error) {
		out := outs[min(*scans, len(outs)-1)]
		*scans++
		return []byte(out), err
	}
	t.Cleanup(func() { attachProcs, agentsViewSettle, knownViews.pids = origProcs, origSettle, nil })
	return home, scans
}

// procs joins process entries the way pgrep is asked to.
func procs(entries ...string) string { return strings.Join(entries, procSep) + "\n" }

func writeSessionFile(t *testing.T, home string, pid int, body string) {
	t.Helper()
	dir := filepath.Join(home, ".claude", "sessions")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, fmt.Sprintf("%d.json", pid)), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// cavWrapper is the shell cav runs an attach in, as a real process list shows
// it: one process over many lines.
const cavWrapper = `76298 sh -c cav_attach() {
	[ -n "$CAV_TITLE" ] && printf '\033[22;0t\033]0;%s\007' "$CAV_TITLE"
	"$CAV_CLAUDE" attach "$1" <&0 & A=$!
	K=0
	while kill -0 "$A" 2>/dev/null; do
		case "$(ps -ww -o args= -p "$A" 2>/dev/null)" in
		*\ agents) kill "$A" 2>/dev/null && K=1 ;;
		esac
		sleep 0.1
	done
	wait "$A"; S=$?
	[ "$K" = 1 ] && return 0
	return "$S"
}
"$CAV_CLAUDE" respawn "$CAV_JOB" && cav_attach "$CAV_JOB"`

func TestParseAttachTargets(t *testing.T) {
	for _, tc := range []struct {
		name string
		proc string
		want []string
	}{
		{"plain", "76300 claude attach 58f5b117", []string{"58f5b117"}},
		{"by path", "80715 /Users/someone/.local/bin/claude attach 8cfd0d41", []string{"8cfd0d41"}},
		{"through a shell", "81000 /bin/sh /tmp/harness/claude attach dddd0001", []string{"dddd0001"}},
		{"flags before attach (a shell alias adds them)", "76301 claude --model fable --effort max attach aaaa0002", []string{"aaaa0002"}},
		{"through an interpreter", "76302 node /opt/claude-code/cli.js attach aaaa0003", []string{"aaaa0003"}},
		{"a job id counts from any binary", "76303 renamed-binary attach aaaa0004", []string{"aaaa0004"}},
		{"a full session id", "76304 claude attach 72cdfc0f-1111-2222-3333-444455556666", []string{"72cdfc0f"}},
		{"flags between attach and the id", "76307 claude attach --verbose aaaa0005", []string{"aaaa0005"}},
		{"a path that holds the word", "76308 /Users/a b/my attach dir/claude attach aaaa0006", []string{"aaaa0006"}},
		{"a newline in an earlier argument", "76309 claude --append-system-prompt line one\nline two attach aaaa0007", []string{"aaaa0007"}},
		{"by name, lower-cased", "76305 claude attach Hermes-Agent", []string{"hermes-agent"}},
		{"by name, through an interpreter with flags", "76313 node --no-warnings /opt/claude-code/cli.js attach hermes", []string{"hermes"}},
		{"by name, an interpreter with many flags", "76318 node --no-warnings --enable-source-maps --max-old-space-size=8192 /opt/claude-code/cli.js attach hermes", []string{"hermes"}},
		{"a short id is kept as a name target", "76314 claude attach 58f5b", []string{"58f5b"}},
		{"a name with spaces: whole and first word", "76306 claude --model fable attach my cool session", []string{"my cool session", "my"}},
		{"a name followed by a flag", "76310 claude attach hermes --verbose", []string{"hermes --verbose", "hermes"}},
		{"a name after a flag and its value", "76315 claude attach --model fable my-session", []string{"fable my-session", "fable", "my-session"}},
		{"by name, a program path with a space", "76316 /Applications/Claude Code/bin/claude attach hermes", []string{"hermes"}},
		{"a name that is a path ending in claude", "76319 claude attach infra/claude", []string{"infra/claude"}},
		{"a flag value that is a path ending in claude", "76320 claude --add-dir /x/claude attach hermes", []string{"hermes"}},
		{"a name after a newline in an earlier argument", "76311 claude --append-system-prompt one\ntwo attach hermes", []string{"hermes"}},
		{"an earlier argument with a line that starts with a number", "76321 claude --append-system-prompt rules:\n2 be terse attach hermes", []string{"hermes"}},
		{"the word twice", "76312 claude --append-system-prompt never attach files attach hermes", []string{"files attach hermes", "files", "hermes"}},
		{"the word in a prompt only protects", "81007 claude fix the attach bug", []string{"bug"}},
		{"a script that only lives under ~/.claude", "76317 python3 /Users/x/.claude/skills/tool.py attach hermes", nil},
		{"the wrapper cav runs an attach in", cavWrapper, nil},
		{"a launcher that only quotes the command", `81001 zsh -c eval 'bash -c "exec -a \"claude attach cccc0001\" sleep 100"'`, nil},
		{"another tool's attach", "81002 docker attach web", nil},
		{"tmux attach", "81006 tmux attach -t work", nil},
		{"attach with nothing after it", "81003 claude attach", nil},
		{"only flags after attach", "81005 claude attach --help", nil},
		{"a file name", "81004 vim notes-about-claude attach.md", nil},
		{"only odd spaces after attach", "81008 foo attach  \v", nil},
	} {
		got, views := parseAttachProcs(procs(tc.proc))
		if len(views) != 0 {
			t.Errorf("%s: taken for an agents view", tc.name)
		}
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: targets = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The native agents view opens sessions inside its own process, so the scan
// must report the view itself.
func TestParseSeesAnAgentsView(t *testing.T) {
	for proc, view := range map[string]bool{
		"100 claude agents":                                     true,
		"101 /Users/someone/.local/bin/claude agents":           true,
		"102 claude --model fable --effort max agents":          true, // a shell alias adds flags
		"103 claude agents --cwd /some/path":                    true,
		"104 claude --verbose agents":                           true,
		"105 node --no-warnings /opt/claude-code/cli.js agents": true,
		"113 /Applications/Claude Code/bin/claude agents":       true, // a path with a space
		"118 claude agents --cwd /Users/x/dotfiles/claude":      true, // a path argument named claude
		"119 node --no-warnings --enable-source-maps --max-old-space-size=8192 /opt/claude-code/cli.js agents": true,
		// Paths that only hold the word claude are not claude.
		"114 python3 /Users/x/.claude/skills/tool.py agents list":    false,
		"115 bash /Users/x/.claude/jobs/abcd0001/tmp/run.sh agents":  false,
		"116 /Users/x/proj/.claude/worktrees/w/bin/mytool agents":    false,
		"117 claude --model fable --effort max tell me about agents": false, // a prompt after alias flags
		"106 claude agents --json":                                   false, // cav's own poll
		"107 claude agents --all --json":                             false,
		"108 claude --model fable agents --json":                     false,
		"109 claude review the agents code":                          false, // a prompt
		"110 claude attach agents":                                   false, // a session named agents
		"111 claude bg-spare --bg-spare /tmp/x.claim.sock":           false,
		"112 vim agents": false,
		"120 pooldemo --title 6 agents, 2 workers": false,
		cavWrapper: false, // its script holds the word
	} {
		targets, views := parseAttachProcs(procs("76300 claude attach 58f5b117", proc))
		if got := len(views) == 1 && views[0] == strings.Fields(proc)[0]; got != view {
			t.Errorf("%q: views = %q, want view = %v", proc, views, view)
		}
		if !slices.Contains(targets, "58f5b117") {
			t.Errorf("%q: targets = %q, the attach beside it is lost", proc, targets)
		}
	}
}

func TestAttachTargetsReadsEveryProcess(t *testing.T) {
	withAttachProcs(t, nil, procs(cavWrapper, "76300 claude attach 58f5b117", cavWrapper, "80715 claude attach Hermes"))

	got, err := AttachTargets()
	if want := []string{"58f5b117", "hermes"}; err != nil || !reflect.DeepEqual(got, want) {
		t.Errorf("AttachTargets() = %q, %v; want %q, nil", got, err, want)
	}
}

func TestAttachTargetsUnknownWhenTheScanFails(t *testing.T) {
	withAttachProcs(t, errors.New("pgrep failed"), "")
	if got, err := AttachTargets(); err == nil || got != nil {
		t.Errorf("AttachTargets() = %v, %v; want nil and an error so callers stop nothing", got, err)
	}
}

func TestAttachTargetsNobodyAttached(t *testing.T) {
	withAttachProcs(t, nil, "")
	got, err := AttachTargets()
	if err != nil || got == nil || len(got) != 0 {
		t.Errorf("AttachTargets() = %v, %v; want an empty, non-nil list and no error", got, err)
	}
}

// ← in an attach turns the attach process into `claude agents` until cav's
// watchdog ends it. Only a view that is still there on a second look is real.
func TestAttachTargetsLooksTwiceAtANewAgentsView(t *testing.T) {
	const attached, view = "76300 claude attach 58f5b117", "80715 claude agents"

	_, scans := withAttachProcs(t, nil, procs(attached, view), procs(attached))
	if got, err := AttachTargets(); err != nil || !reflect.DeepEqual(got, []string{"58f5b117"}) || *scans != 2 {
		t.Errorf("a view gone on the second look: got %q, %v after %d scans; want the attach, no error, 2 scans", got, err, *scans)
	}

	_, scans = withAttachProcs(t, nil, procs(attached, view))
	if got, err := AttachTargets(); !errors.Is(err, ErrAgentsView) || got != nil || *scans != 2 {
		t.Errorf("a view that stays: got %q, %v after %d scans; want ErrAgentsView after 2 scans", got, err, *scans)
	}
	// A view already confirmed needs no second look: it would slow every refresh.
	if _, err := AttachTargets(); !errors.Is(err, ErrAgentsView) || *scans != 3 {
		t.Errorf("the same view again: %v after %d scans in all; want ErrAgentsView with one more scan", err, *scans)
	}
}

// ← in a plain interactive claude opens the agents view inside that process.
// Its session file then holds the job it parked: the only sign of the view.
func TestAttachTargetsSeesAViewInsideAClaudeProcess(t *testing.T) {
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Skip("cannot run a helper process: " + err.Error())
	}
	me := os.Getpid()
	started := procStart(me)
	if started == "" {
		t.Skip("cannot read a process start time here")
	}
	for _, tc := range []struct {
		name string
		pid  int
		body string
		view bool
	}{
		{"a running process with a parked job", me, fmt.Sprintf(`{"pid":%d,"procStart":%q,"parkedJobId":"abcd0001"}`, me, started), true},
		{"a record with no start time", me, fmt.Sprintf(`{"pid":%d,"parkedJobId":"abcd0001"}`, me), true},
		{"nothing parked", me, fmt.Sprintf(`{"pid":%d,"procStart":%q}`, me, started), false},
		{"the pid now belongs to another process", me, fmt.Sprintf(`{"pid":%d,"procStart":"Mon Jan  1 00:00:00 2001","parkedJobId":"abcd0001"}`, me), false},
		{"a process that is gone", gone.Process.Pid, fmt.Sprintf(`{"pid":%d,"parkedJobId":"abcd0001"}`, gone.Process.Pid), false},
		{"a half-written record", me, fmt.Sprintf(`{"pid":%d,"parkedJobId":"abcd`, me), false},
	} {
		home, _ := withAttachProcs(t, nil, procs("76300 claude attach 58f5b117"))
		writeSessionFile(t, home, tc.pid, tc.body)

		got, err := AttachTargets()
		if errors.Is(err, ErrAgentsView) != tc.view {
			t.Errorf("%s: got %q, %v; want view = %v", tc.name, got, err, tc.view)
		}
		if !tc.view && !reflect.DeepEqual(got, []string{"58f5b117"}) {
			t.Errorf("%s: targets = %q, want the attach", tc.name, got)
		}
	}
}

// The real scan: stand-in processes with an attach command line must be found,
// by id and by name, also when flags come before `attach`.
func TestAttachTargetsFindsRealProcesses(t *testing.T) {
	if _, err := AttachTargets(); err != nil {
		t.Skip("cannot tell who is attached here: " + err.Error())
	}
	// Random targets, so two test runs at the same time do not see each other.
	id, name := fmt.Sprintf("%08x", rand.Uint32()), fmt.Sprintf("cav-standin-%08x", rand.Uint32())
	for _, tc := range []struct{ argv0, want string }{
		{"claude attach " + id, id},
		{"claude --model fable attach " + name, name},
	} {
		if got, _ := AttachTargets(); slices.Contains(got, tc.want) {
			t.Fatalf("before: targets = %q already hold %s", got, tc.want)
		}
		// exec -a sets the whole command line; sleep's own argument follows it.
		cmd := exec.Command("bash", "-c", `exec -a "$0" sleep 30`, tc.argv0)
		if err := cmd.Start(); err != nil {
			t.Skip("cannot start the stand-in process: " + err.Error())
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

		if !waitForTarget(tc.want) {
			t.Errorf("a running `%s` process was not reported", tc.argv0)
		}
	}
}

func waitForTarget(want string) bool {
	for i := 0; i < 50; i++ {
		if got, err := AttachTargets(); err == nil && slices.Contains(got, want) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
