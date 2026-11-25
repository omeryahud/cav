package claude

import (
	"errors"
	"os/exec"
	"regexp"
	"strings"
)

// attachProcs lists the processes whose command line has an `attach` argument,
// one "pid args" line each. It is a package variable so tests can stub it.
var attachProcs = func() ([]byte, error) {
	out, err := exec.Command("pgrep", "-fl", " attach ").Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil, nil // pgrep exits 1 when nothing matches
	}
	return out, err
}

var (
	// attachRE splits a process line into the command before `attach` and the
	// text after it.
	attachRE = regexp.MustCompile(`(?m)^[ \t]*\d+[ \t]+(.*?)[ \t]attach[ \t]+(\S.*)$`)
	// jobIDRE matches a job id, alone or as the start of a full session id.
	jobIDRE = regexp.MustCompile(`^[0-9a-f]{8}(?:-[0-9a-f-]*)?$`)
)

// AttachTargets returns what every running attach was asked to open, in lower
// case: a job id, or the name (or part of the name) of a session, because
// `claude attach` takes both. ok is false when the process list could not be
// read, so a caller can tell "nobody is attached" from "unknown".
func AttachTargets() (targets []string, ok bool) {
	out, err := attachProcs()
	if err != nil {
		return nil, false
	}
	return parseAttachTargets(out), true
}

func parseAttachTargets(out []byte) []string {
	targets := []string{}
	for _, m := range attachRE.FindAllSubmatch(out, -1) {
		cmd := strings.Fields(strings.ToLower(string(m[1])))
		rest := strings.ToLower(strings.TrimSpace(string(m[2])))
		first := strings.Fields(rest)[0]
		switch {
		case jobIDRE.MatchString(first):
			targets = append(targets, first[:8]) // a job id is trusted from any binary
		case runsClaude(cmd) && !strings.HasPrefix(rest, "-"):
			targets = append(targets, rest)
		}
	}
	return targets
}

// runsClaude reports whether a command line starts claude, directly or through
// an interpreter (`node …/claude-code/cli.js`).
func runsClaude(cmd []string) bool {
	for i := 0; i < len(cmd) && i < 2; i++ {
		if strings.Contains(cmd[i], "claude") {
			return true
		}
	}
	return false
}
