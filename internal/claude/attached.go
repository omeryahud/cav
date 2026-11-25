package claude

import (
	"errors"
	"os/exec"
	"regexp"
	"strings"
)

// attachProcs lists the processes whose command line has an `attach` argument,
// one "pid args" entry each. It is a package variable so tests can stub it.
var attachProcs = func() ([]byte, error) {
	out, err := exec.Command("pgrep", "-fl", " attach ").Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil, nil // pgrep exits 1 when nothing matches
	}
	return out, err
}

var (
	// procStartRE finds where each process starts in the pgrep output: a pid at
	// the start of a line. An argument can hold a newline, so one process can
	// take several lines.
	procStartRE = regexp.MustCompile(`(?m)^[ \t]*\d+[ \t]`)
	// jobIDRE matches a job id, alone or as the start of a full session id.
	jobIDRE = regexp.MustCompile(`^[0-9a-f]{8}(?:-[0-9a-f-]*)?$`)
)

// AttachTargets returns what every running attach was asked to open: a job id,
// or a session name in lower case, because `claude attach` takes both (and part
// of a name is enough). ok is false when the process list could not be read,
// so a caller can tell "nobody is attached" from "unknown".
func AttachTargets() (targets []string, ok bool) {
	out, err := attachProcs()
	if err != nil {
		return nil, false
	}
	return parseAttachTargets(string(out)), true
}

func parseAttachTargets(out string) []string {
	targets := []string{}
	for _, proc := range splitProcs(out) {
		ids := attachedIDs(proc)
		if len(ids) == 0 {
			ids = attachedNames(proc)
		}
		targets = append(targets, ids...)
	}
	return targets
}

func splitProcs(out string) []string {
	starts := procStartRE.FindAllStringIndex(out, -1)
	procs := make([]string, 0, len(starts))
	for i, s := range starts {
		end := len(out)
		if i+1 < len(starts) {
			end = starts[i+1][0]
		}
		procs = append(procs, out[s[0]:end])
	}
	return procs
}

// attachedIDs returns the job ids that follow an `attach` argument on the same
// line. A job id is trusted from any binary and with any flags around it.
func attachedIDs(proc string) []string {
	var ids []string
	for _, line := range strings.Split(proc, "\n") {
		seen := false
		for _, w := range strings.Fields(line) {
			if w == "attach" {
				seen = true
			} else if seen && jobIDRE.MatchString(w) {
				ids = append(ids, w[:8])
				break
			}
		}
	}
	return ids
}

// attachedNames returns the session name a `claude attach <name>` was given:
// the whole text after attach (a name can hold spaces) and its first word (in
// case more arguments follow the name). A claude that only has the word in its
// prompt is picked up too. That is harmless: it can only keep a session alive.
func attachedNames(proc string) []string {
	words := strings.Fields(strings.ToLower(proc))
	if len(words) < 2 {
		return nil
	}
	cmd := words[1:] // words[0] is the pid
	if !runsClaude(cmd) {
		return nil
	}
	var names []string
	for i := 1; i < len(cmd); i++ {
		if cmd[i] != "attach" {
			continue
		}
		name := cmd[i+1:]
		for len(name) > 0 && strings.HasPrefix(name[0], "-") {
			name = name[1:]
		}
		if len(name) > 0 {
			names = append(names, strings.Join(name, " "))
		}
		if len(name) > 1 {
			names = append(names, name[0])
		}
	}
	return names
}

// runsClaude reports whether cmd starts claude, directly or as the script an
// interpreter runs (`node [flags] …/claude-code/cli.js`).
func runsClaude(cmd []string) bool {
	if strings.Contains(cmd[0], "claude") {
		return true
	}
	for _, w := range cmd[1:] {
		if !strings.HasPrefix(w, "-") {
			return strings.Contains(w, "claude")
		}
	}
	return false
}
