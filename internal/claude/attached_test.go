package claude

import (
	"errors"
	"os/exec"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func withAttachProcs(t *testing.T, out string, err error) {
	t.Helper()
	orig := attachProcs
	attachProcs = func() ([]byte, error) { return []byte(out), err }
	t.Cleanup(func() { attachProcs = orig })
}

func TestAttachTargets(t *testing.T) {
	withAttachProcs(t, `76300 claude attach 58f5b117
80715 /Users/someone/.local/bin/claude attach 8cfd0d41
81000 /bin/sh /tmp/harness/claude attach dddd0001
76301 claude --model fable --effort max attach aaaa0002
76302 node /opt/claude-code/cli.js attach aaaa0003
76303 renamed-binary attach aaaa0004
76304 claude attach 72cdfc0f-1111-2222-3333-444455556666
76305 claude attach Hermes-Agent
76306 claude --model fable attach my cool session
76298 sh -c cav_attach() { "$CAV_CLAUDE" attach "$1" <&0 & A=$! ; } ; cav_attach "$CAV_JOB"
81001 zsh -c eval 'bash -c "exec -a \"claude attach cccc0001\" sleep 100"'
81002 docker attach web
81003 claude attach
81004 vim notes-about-claude attach.md
81005 claude attach --help
81006 tmux attach -t work
`, nil)

	got, ok := AttachTargets()
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{
		"58f5b117", "8cfd0d41", "dddd0001", // plain, by path, through a shell
		"aaaa0002",        // flags before attach (a shell alias adds them)
		"aaaa0003",        // through an interpreter
		"aaaa0004",        // a job id counts from any binary
		"72cdfc0f",        // a full session id
		"hermes-agent",    // by name, lower-cased
		"my cool session", // a quoted name with spaces
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("targets =\n  %q\nwant\n  %q", got, want)
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
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not installed")
	}
	for _, tc := range []struct{ argv0, want string }{
		{"claude attach 0badf00d", "0badf00d"},
		{"claude --model fable attach cav-standin-name", "cav-standin-name"},
	} {
		if got, ok := AttachTargets(); !ok || slices.Contains(got, tc.want) {
			t.Fatalf("before: targets=%v ok=%v, want ok and no %s", got, ok, tc.want)
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

func waitForTarget(prefix string) bool {
	for i := 0; i < 50; i++ {
		got, ok := AttachTargets()
		if ok && slices.ContainsFunc(got, func(s string) bool { return strings.HasPrefix(s, prefix) }) {
			return true
		}
		time.Sleep(100 * time.Millisecond)
	}
	return false
}
