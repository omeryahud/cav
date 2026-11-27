package claude

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ErrAgentsView means a native agents view is open. It shows sessions inside
// its own process, so no attach process tells which session is on screen.
var ErrAgentsView = errors.New("a claude agents view is open")

// agentsViewSettle is how long a new `claude agents` process must last to
// count as a view: ← in an attach passes through one until cav's watchdog ends it.
var agentsViewSettle = 500 * time.Millisecond

// procSep separates the processes in the pgrep output. A newline cannot: an
// argument can hold one.
const procSep = "\x1e"

// attachProcs lists this user's processes that have an `attach` or `agents`
// argument, as "pid args" entries joined by procSep. Tests stub it.
var attachProcs = func() ([]byte, error) {
	out, err := exec.Command("pgrep", "-fl", "-U", strconv.Itoa(os.Getuid()), "-d", procSep, " (attach |agents)").Output()
	var exit *exec.ExitError
	if errors.As(err, &exit) && exit.ExitCode() == 1 {
		return nil, nil // pgrep exits 1 when nothing matches
	}
	return out, err
}

// jobIDRE matches a job id, alone or as the start of a full session id.
var jobIDRE = regexp.MustCompile(`^[0-9a-f]{8}(?:-[0-9a-f-]*)?$`)

// knownViews holds the pids of the agents views the last scan confirmed.
var knownViews = struct {
	sync.Mutex
	pids []string
}{}

// AttachTargets returns what each running attach was asked to open: a job id,
// or (part of) a session name in lower case. An error means it cannot tell.
func AttachTargets() ([]string, error) {
	targets, views, err := scanAttach()
	if err == nil && newView(views) {
		time.Sleep(agentsViewSettle)
		targets, views, err = scanAttach()
	}
	if err != nil {
		return nil, err
	}
	knownViews.Lock()
	knownViews.pids = views
	knownViews.Unlock()
	if len(views) > 0 || parkedView() {
		return nil, ErrAgentsView
	}
	return targets, nil
}

// newView reports whether a view was not there at the last scan.
func newView(views []string) bool {
	knownViews.Lock()
	defer knownViews.Unlock()
	return slices.ContainsFunc(views, func(pid string) bool { return !slices.Contains(knownViews.pids, pid) })
}

func scanAttach() (targets, views []string, err error) {
	out, err := attachProcs()
	if err != nil {
		return nil, nil, fmt.Errorf("process scan: %w", err)
	}
	targets, views = parseAttachProcs(string(out))
	return targets, views, nil
}

// parseAttachProcs returns the attach targets and the pids of the agents views.
func parseAttachProcs(out string) (targets, views []string) {
	targets = []string{}
	for _, proc := range strings.Split(out, procSep) {
		words := strings.Fields(strings.ToLower(proc))
		if len(words) < 2 {
			continue
		}
		args, isClaude := claudeArgs(words[1:]) // words[0] is the pid
		if isClaude && agentsView(args) {
			views = append(views, words[0])
			continue
		}
		ids := attachedIDs(proc)
		if len(ids) == 0 && isClaude {
			ids = attachedNames(args)
		}
		targets = append(targets, ids...)
	}
	return targets, views
}

// programWords is how many leading words, flags aside, can hold the program:
// an interpreter and a path cut in two by a space.
const programWords = 4

// claudeArgs returns the arguments claude was started with, or ok false when
// cmd does not run claude. Only the base name counts: a script under ~/.claude is not claude.
func claudeArgs(cmd []string) (args []string, ok bool) {
	at, seen := -1, 0
	for i, w := range cmd {
		if w == "attach" || w == "agents" {
			break // the subcommand: the program stands before it
		}
		if strings.HasPrefix(w, "-") {
			continue
		}
		if seen++; seen > programWords {
			break
		}
		// A later path replaces the program only right after it: a path with a space.
		if isClaudeProgram(w, i == 0) && (at < 0 || at == i-1) {
			at = i
		}
	}
	if at < 0 {
		return nil, false
	}
	return cmd[at+1:], true
}

// isClaudeProgram reports whether w names the claude program: `claude`, or
// `cli.js` in a claude-code package. A bare word counts only as the first one.
func isClaudeProgram(w string, first bool) bool {
	slash := strings.LastIndex(w, "/")
	base := w[slash+1:]
	named := base == "claude" || (base == "cli.js" && strings.Contains(w, "claude-code"))
	return named && (first || slash >= 0)
}

// agentsView reports whether args start the native agents view: `agents` as
// the subcommand, without `--json` (that only prints the list, as cav's poll does).
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

// parkedView reports whether a claude shows the agents view inside itself: ←
// in a plain claude parks the conversation and notes the job in its session file.
func parkedView() bool {
	recs, _ := readSessionRecords()
	for _, r := range recs {
		if r.ParkedJobID != "" && sameProcess(r.PID, r.ProcStart) {
			return true
		}
	}
	return false
}

// procStarts returns when each pid started, as claude writes it in a session
// file (the UTC form of `ps -o lstart`), with one ps for all. Tests stub it.
var procStarts = func(pids []int) map[int]string {
	list := make([]string, len(pids))
	for i, pid := range pids {
		list[i] = strconv.Itoa(pid)
	}
	cmd := exec.Command("ps", "-o", "pid=,lstart=", "-p", strings.Join(list, ","))
	cmd.Env = append(os.Environ(), "TZ=UTC", "LC_ALL=C")
	out, _ := cmd.Output()
	starts := make(map[int]string, len(pids))
	for _, line := range strings.Split(string(out), "\n") {
		if f := strings.Fields(line); len(f) > 1 {
			if pid, err := strconv.Atoi(f[0]); err == nil {
				starts[pid] = strings.Join(f[1:], " ")
			}
		}
	}
	return starts
}

// procStart returns when pid started, or "" when pid is not running.
func procStart(pid int) string { return procStarts([]int{pid})[pid] }

// sameProcess reports whether pid still is the process that wrote a record: a
// record can outlive a crash, and the pid can go to another process.
func sameProcess(pid int, started string) bool {
	if pid <= 0 {
		return false
	}
	now := procStart(pid)
	return now != "" && (started == "" || now == strings.Join(strings.Fields(started), " "))
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

// attachedNames returns the names `claude attach` may have been given: the
// text after it, its first word, and each word when flags stand among them.
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
