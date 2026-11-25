package claude

import (
	"errors"
	"fmt"
	"os/exec"
	"regexp"
	"slices"
	"strings"
)

// ErrAgentsView is returned while a native `claude agents` view is open. The
// view attaches to sessions inside its own process, so no attach process tells
// which session it has on screen.
var ErrAgentsView = errors.New("a claude agents view is open")

// attachProcs lists the processes whose command line has an `attach` or an
// `agents` argument, one "pid args" entry each. It is a package variable so
// tests can stub it.
var attachProcs = func() ([]byte, error) {
	out, err := exec.Command("pgrep", "-fl", " (attach |agents)").Output()
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
// of a name is enough). It returns an error when it cannot tell who is
// attached: the process list could not be read, or a native agents view is
// open (ErrAgentsView). An empty list with no error means nobody is attached.
func AttachTargets() ([]string, error) {
	out, err := attachProcs()
	if err != nil {
		return nil, fmt.Errorf("process scan: %w", err)
	}
	return parseAttachTargets(string(out))
}

func parseAttachTargets(out string) ([]string, error) {
	targets := []string{}
	for _, proc := range splitProcs(out) {
		words := strings.Fields(strings.ToLower(proc))
		args, isClaude := claudeArgs(words[1:]) // words[0] is the pid
		if isClaude && agentsView(args) {
			return nil, ErrAgentsView
		}
		ids := attachedIDs(proc)
		if len(ids) == 0 && isClaude {
			ids = attachedNames(args)
		}
		targets = append(targets, ids...)
	}
	return targets, nil
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

// claudeArgs returns the arguments claude was started with. ok is false when
// cmd does not run claude, directly or as the script an interpreter runs
// (`node [flags] …/claude-code/cli.js`).
func claudeArgs(cmd []string) (args []string, ok bool) {
	for i, w := range cmd {
		if i > 0 && strings.HasPrefix(w, "-") {
			continue // an interpreter flag
		}
		if strings.Contains(w, "claude") {
			return cmd[i+1:], true
		}
		if i > 0 {
			break // the script the interpreter runs is not claude
		}
	}
	return nil, false
}

// agentsView reports whether args start the native agents view: `agents` as
// the subcommand (only flags and their values before it) and no `--json`,
// which only prints the list and exits, as cav's own poll does.
func agentsView(args []string) bool {
	for i, w := range args {
		if w == "agents" {
			return !slices.Contains(args[i+1:], "--json")
		}
		if !strings.HasPrefix(w, "-") && (i == 0 || !strings.HasPrefix(args[i-1], "-")) {
			return false // a prompt or another subcommand
		}
	}
	return false
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
func attachedNames(args []string) []string {
	var names []string
	for i, w := range args {
		if w != "attach" {
			continue
		}
		name := args[i+1:]
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
