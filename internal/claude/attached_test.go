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
	"testing"
	"time"
)

// withAttachProcs stubs the process list, one output per scan (the last one
// repeats), in an empty home so no real session file is read. It returns the
// home directory.
func withAttachProcs(t *testing.T, err error, outs ...string) (home string) {
	t.Helper()
	home = t.TempDir()
	t.Setenv("HOME", home)
	origProcs, origSettle := attachProcs, agentsViewSettle
	agentsViewSettle = 0
	scan := 0
	attachProcs = func() ([]byte, error) {
		out := outs[min(scan, len(outs)-1)]
		scan++
		return []byte(out), err
	}
	t.Cleanup(func() { attachProcs, agentsViewSettle = origProcs, origSettle })
	return home
}

// writeSessionFile writes a claude process record under home.
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

// cavWrapper is what `pgrep -fl` prints for the shell cav runs an attach in,
// copied from a real process list: one process, many lines.
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
"$CAV_CLAUDE" respawn "$CAV_JOB" && cav_attach "$CAV_JOB"
`

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
		{"a short id is kept as a name target", "76314 claude attach 58f5b", []string{"58f5b"}},
		{"a name with spaces: whole and first word", "76306 claude --model fable attach my cool session", []string{"my cool session", "my"}},
		{"a name followed by a flag", "76310 claude attach hermes --verbose", []string{"hermes --verbose", "hermes"}},
		{"a name after a flag and its value", "76315 claude attach --model fable my-session", []string{"fable my-session", "fable", "my-session"}},
		{"by name, a program path with a space", "76316 /Applications/Claude Code/bin/claude attach hermes", []string{"hermes"}},
		{"a script that only lives under ~/.claude", "76317 python3 /Users/x/.claude/skills/tool.py attach hermes", nil},
		{"a name after a newline in an earlier argument", "76311 claude --append-system-prompt one\ntwo attach hermes", []string{"hermes"}},
		{"the word twice", "76312 claude --append-system-prompt never attach files attach hermes", []string{"files attach hermes", "files", "hermes"}},
		{"the word in a prompt only protects", "81007 claude fix the attach bug", []string{"bug"}},
		{"the wrapper cav runs an attach in", cavWrapper, nil},
		{"a launcher that only quotes the command", `81001 zsh -c eval 'bash -c "exec -a \"claude attach cccc0001\" sleep 100"'`, nil},
		{"another tool's attach", "81002 docker attach web", nil},
		{"tmux attach", "81006 tmux attach -t work", nil},
		{"attach with nothing after it", "81003 claude attach", nil},
		{"only flags after attach", "81005 claude attach --help", nil},
		{"a file name", "81004 vim notes-about-claude attach.md", nil},
		{"only odd spaces after attach", "81008 foo attach  \v", nil},
	} {
		got, err := parseAttachTargets(tc.proc + "\n")
		if err != nil {
			t.Errorf("%s: err = %v", tc.name, err)
		}
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: targets = %q, want %q", tc.name, got, tc.want)
		}
	}
}

// The native agents view opens sessions inside its own process: no attach
// process shows which one. While a view is open the scan must say it cannot
// tell, even when it also sees ordinary attaches.
func TestAgentsViewMakesTheScanUnknown(t *testing.T) {
	for proc, view := range map[string]bool{
		"100 claude agents":                                     true,
		"101 /Users/someone/.local/bin/claude agents":           true,
		"102 claude --model fable --effort max agents":          true, // a shell alias adds flags
		"103 claude agents --cwd /some/path":                    true,
		"104 claude --verbose agents":                           true,
		"105 node --no-warnings /opt/claude-code/cli.js agents": true,
		"113 /Applications/Claude Code/bin/claude agents":       true, // a path with a space
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
		cavWrapper:       false, // its script holds the word
	} {
		targets, err := parseAttachTargets("76300 claude attach 58f5b117\n" + proc + "\n")
		if got := errors.Is(err, ErrAgentsView); got != view {
			t.Errorf("%q: agents view = %v, want %v", proc, got, view)
		}
		if view && targets != nil {
			t.Errorf("%q: targets = %q, want none with the error", proc, targets)
		}
	}
}

func TestAttachTargetsReadsEveryProcess(t *testing.T) {
	withAttachProcs(t, nil, cavWrapper+"76300 claude attach 58f5b117\n"+cavWrapper+"80715 claude attach Hermes\n")

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

// Stepping out of an attach with ← turns the attach process into `claude
// agents` for a moment, until cav's watchdog ends it. That is not an open
// view: it must not pause auto-stop. A view that is still there on a second
// look is real.
func TestAttachTargetsLooksTwiceAtAnAgentsView(t *testing.T) {
	const attached = "76300 claude attach 58f5b117\n"

	withAttachProcs(t, nil, attached+"80715 claude agents\n", attached)
	if got, err := AttachTargets(); err != nil || !reflect.DeepEqual(got, []string{"58f5b117"}) {
		t.Errorf("a view that is gone on the second look: got %q, %v; want the attach and no error", got, err)
	}

	withAttachProcs(t, nil, attached+"80715 claude agents\n")
	if got, err := AttachTargets(); !errors.Is(err, ErrAgentsView) || got != nil {
		t.Errorf("a view that stays: got %q, %v; want ErrAgentsView", got, err)
	}
}

// ← in a plain interactive claude opens the agents view inside that process:
// its command line shows nothing. The process notes the job it parked in its
// session file, and that is the sign of an open view.
func TestAttachTargetsSeesAViewInsideAClaudeProcess(t *testing.T) {
	gone := exec.Command("true")
	if err := gone.Run(); err != nil {
		t.Skip("cannot run a helper process: " + err.Error())
	}
	for _, tc := range []struct {
		name string
		pid  int
		body string
		view bool
	}{
		{"a running process with a parked job", os.Getpid(), `{"pid":%d,"kind":"interactive","parkedJobId":"abcd0001"}`, true},
		{"a running process with nothing parked", os.Getpid(), `{"pid":%d,"kind":"interactive"}`, false},
		{"a record left by a process that is gone", gone.Process.Pid, `{"pid":%d,"parkedJobId":"abcd0001"}`, false},
		{"a half-written record", os.Getpid(), `{"pid":%d,"parkedJobId":"abcd`, false},
	} {
		home := withAttachProcs(t, nil, "76300 claude attach 58f5b117\n")
		writeSessionFile(t, home, tc.pid, fmt.Sprintf(tc.body, tc.pid))

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
