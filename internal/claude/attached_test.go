package claude

import (
	"errors"
	"fmt"
	"math/rand"
	"os/exec"
	"reflect"
	"slices"
	"testing"
	"time"
)

func withAttachProcs(t *testing.T, out string, err error) {
	t.Helper()
	orig := attachProcs
	attachProcs = func() ([]byte, error) { return []byte(out), err }
	t.Cleanup(func() { attachProcs = orig })
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
		got := parseAttachTargets(tc.proc + "\n")
		if len(got) == 0 && len(tc.want) == 0 {
			continue
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: targets = %q, want %q", tc.name, got, tc.want)
		}
	}
}

func TestAttachTargetsReadsEveryProcess(t *testing.T) {
	withAttachProcs(t, cavWrapper+"76300 claude attach 58f5b117\n"+cavWrapper+"80715 claude attach Hermes\n", nil)

	got, ok := AttachTargets()
	if want := []string{"58f5b117", "hermes"}; !ok || !reflect.DeepEqual(got, want) {
		t.Errorf("AttachTargets() = %q, %v; want %q, true", got, ok, want)
	}
}

func TestAttachTargetsUnknownWhenTheScanFails(t *testing.T) {
	withAttachProcs(t, "", errors.New("pgrep failed"))
	if got, ok := AttachTargets(); ok || got != nil {
		t.Errorf("AttachTargets() = %v, %v; want nil, false so callers stop nothing", got, ok)
	}
}

func TestAttachTargetsNobodyAttached(t *testing.T) {
	withAttachProcs(t, "", nil)
	got, ok := AttachTargets()
	if !ok || got == nil || len(got) != 0 {
		t.Errorf("AttachTargets() = %v, %v; want an empty, non-nil list and true", got, ok)
	}
}

// The real scan: stand-in processes with an attach command line must be found,
// by id and by name, also when flags come before `attach`.
func TestAttachTargetsFindsRealProcesses(t *testing.T) {
	if _, ok := AttachTargets(); !ok {
		t.Skip("the process list cannot be read here")
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
		if got, ok := AttachTargets(); ok && slices.Contains(got, want) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
