package claude

import (
	"errors"
	"os/exec"
	"testing"
	"time"
)

func withAttachProcs(t *testing.T, out string, err error) {
	t.Helper()
	orig := attachProcs
	attachProcs = func() ([]byte, error) { return []byte(out), err }
	t.Cleanup(func() { attachProcs = orig })
}

func TestAttachedJobs(t *testing.T) {
	withAttachProcs(t, `76300 claude attach 58f5b117
80715 /Users/someone/.local/bin/claude attach 8cfd0d41
81000 /bin/sh /tmp/harness/claude attach dddd0001
76298 sh -c cav_attach() { "$CAV_CLAUDE" attach "$1" <&0 & A=$! ; } ; cav_attach "$CAV_JOB"
81001 zsh -c eval 'bash -c "exec -a \"claude attach cccc0001\" sleep 100"'
81002 notclaude attach bbbb0001
81003 claude attach
81004 vim notes-about-claude attach.md
81005 claude attach --help
`, nil)

	jobs, ok := AttachedJobs()
	if !ok {
		t.Fatal("ok = false, want true")
	}
	want := []string{"58f5b117", "8cfd0d41", "dddd0001"}
	if len(jobs) != len(want) {
		t.Errorf("jobs = %v, want exactly %v", jobs, want)
	}
	for _, id := range want {
		if !jobs[id] {
			t.Errorf("job %s should be reported as attached", id)
		}
	}
}

func TestAttachedJobsUnknownWhenTheScanFails(t *testing.T) {
	withAttachProcs(t, "", errors.New("pgrep failed"))
	if jobs, ok := AttachedJobs(); ok || jobs != nil {
		t.Errorf("AttachedJobs() = %v, %v; want nil, false so callers stop nothing", jobs, ok)
	}
}

func TestAttachedJobsNobodyAttached(t *testing.T) {
	withAttachProcs(t, "", nil)
	jobs, ok := AttachedJobs()
	if !ok || jobs == nil || len(jobs) != 0 {
		t.Errorf("AttachedJobs() = %v, %v; want an empty, non-nil set and true", jobs, ok)
	}
}

// The real scan: this test's own process list has no attach whose job id is
// made up, and a stand-in process with such a command line must be found.
func TestAttachedJobsFindsARealProcess(t *testing.T) {
	if _, err := exec.LookPath("pgrep"); err != nil {
		t.Skip("pgrep not installed")
	}
	if jobs, ok := AttachedJobs(); !ok || jobs["0badf00d"] {
		t.Fatalf("before: jobs=%v ok=%v, want ok and no 0badf00d", jobs, ok)
	}
	cmd := exec.Command("bash", "-c", `exec -a "claude attach 0badf00d" sleep 30`)
	if err := cmd.Start(); err != nil {
		t.Skip("cannot start the stand-in process: " + err.Error())
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })

	for i := 0; i < 50; i++ {
		if jobs, ok := AttachedJobs(); ok && jobs["0badf00d"] {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Error("a running `claude attach 0badf00d` process was not reported as attached")
}
