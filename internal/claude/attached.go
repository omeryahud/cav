package claude

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"syscall"
	"time"
)

// ErrAgentsView is returned while a native `claude agents` view is open. The
// view attaches to sessions inside its own process, so no attach process tells
// which session it has on screen.
var ErrAgentsView = errors.New("a claude agents view is open")

// agentsViewSettle is how long a `claude agents` process must last before it
// counts as a view. Stepping out of an attach with ← turns the attach into one
// for a moment, until cav's watchdog ends it.
var agentsViewSettle = 500 * time.Millisecond

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
	targets, err := scanAttach()
	if errors.Is(err, ErrAgentsView) {
		time.Sleep(agentsViewSettle)
		targets, err = scanAttach()
	}
	if err == nil && parkedView() {
		return nil, ErrAgentsView
	}
	return targets, err
}

func scanAttach() ([]string, error) {
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

// programWords is how many leading words of a command line can hold the
// program: an interpreter, its flags, and a path cut in two by a space.
const programWords = 4

// claudeArgs returns the arguments claude was started with. ok is false when
// cmd does not run claude. The program is known by its base name, `claude` or
// `cli.js` inside a claude-code package, so a script that only lives under
// ~/.claude does not count. It is the first word, or a later path among the
// leading words (`node [flags] …/cli.js`, `/bin/sh …/claude`, a path with a
// space); the last such path wins.
func claudeArgs(cmd []string) (args []string, ok bool) {
	at := -1
	for i, w := range cmd {
		if i == programWords {
			break
		}
		slash := strings.LastIndex(w, "/")
		base := w[slash+1:]
		isClaude := base == "claude" || (base == "cli.js" && strings.Contains(w, "claude-code"))
		if isClaude && (i == 0 || slash >= 0) {
			at = i
		}
	}
	if at < 0 {
		return nil, false
	}
	return cmd[at+1:], true
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

// parkedView reports whether a claude process shows the agents view inside
// itself. Pressing ← in a plain interactive claude parks that conversation as
// a job and opens the view in the same process, so there is no `agents`
// argument to see. Meanwhile the process keeps the parked job's id in its own
// record under ~/.claude/sessions.
func parkedView() bool {
	h, err := os.UserHomeDir()
	if err != nil {
		return false
	}
	files, _ := filepath.Glob(filepath.Join(h, ".claude", "sessions", "*.json"))
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var rec struct {
			PID         int    `json:"pid"`
			ParkedJobID string `json:"parkedJobId"`
		}
		if json.Unmarshal(b, &rec) == nil && rec.ParkedJobID != "" && processAlive(rec.PID) {
			return true
		}
	}
	return false
}

// processAlive reports whether pid is a running process. A record can outlive
// a process that crashed.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	err := syscall.Kill(pid, 0)
	return err == nil || errors.Is(err, syscall.EPERM)
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

// attachedNames returns the session names a `claude attach <name>` may have
// been given: the whole text after attach (a name can hold spaces) and its
// first word. When flags stand among the words, each word can be the name
// (`attach --model fable my-session`), so each one is returned. A claude that
// only has the word in its prompt is picked up too. Every extra name is
// harmless: it can only keep a session alive.
func attachedNames(args []string) []string {
	var names []string
	add := func(name string) {
		if !slices.Contains(names, name) {
			names = append(names, name)
		}
	}
	for i, w := range args {
		if w != "attach" {
			continue
		}
		var words []string
		flags := false
		for _, r := range args[i+1:] {
			if strings.HasPrefix(r, "-") {
				flags = true
			} else {
				words = append(words, r)
			}
		}
		if len(words) == 0 {
			continue
		}
		rest := args[i+1:]
		for strings.HasPrefix(rest[0], "-") {
			rest = rest[1:]
		}
		add(strings.Join(rest, " "))
		add(words[0])
		if flags {
			for _, word := range words {
				add(word)
			}
		}
	}
	return names
}
