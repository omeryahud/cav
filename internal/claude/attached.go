package claude

import (
	"errors"
	"os/exec"
	"regexp"
)

// attachProcs lists the processes whose command line mentions `claude attach`,
// one "pid args" line each. It is a package variable so tests can stub it.
var attachProcs = func() ([]byte, error) {
	out, err := exec.Command("pgrep", "-fl", "claude attach").Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil, nil // pgrep exits 1 when nothing matches
	}
	return out, err
}

var attachRE = regexp.MustCompile(`(?m)(?:^|[ \t/])claude[ \t]+attach[ \t]+([0-9a-f]{8})`)

// AttachedJobs returns the ids of the jobs a terminal is attached to right now:
// each one has a running `claude attach <jobId>`. ok is false when the process
// list could not be read, so a caller can tell "nobody is attached" from
// "unknown".
func AttachedJobs() (jobs map[string]bool, ok bool) {
	out, err := attachProcs()
	if err != nil {
		return nil, false
	}
	jobs = map[string]bool{}
	for _, m := range attachRE.FindAllSubmatch(out, -1) {
		jobs[string(m[1])] = true
	}
	return jobs, true
}
