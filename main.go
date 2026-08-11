// Command cav is a custom Claude Code agents view: a terminal UI to list,
// create, delete, reorder, search, and open background Claude sessions.
package main

import (
	"errors"
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/omeryahud/cav/internal/ui"
)

const usage = `usage: cav [term]            open with the list filtered to <term>
       cav -o <name>        open the session named <name> (--open)
       cav -n [name] [-a]   new session in the current dir (--new; -a/--attach
                            drops you straight into it)
`

// parseArgs maps the CLI onto ui.Options. -a/--attach may appear anywhere but
// only means something with -n. An empty error message means "print usage and
// exit 0" (-h); a non-empty one is a usage error (exit 2).
func parseArgs(args []string, cwd string) (ui.Options, error) {
	var opts ui.Options
	attach := false
	rest := make([]string, 0, len(args))
	for _, a := range args {
		if a == "-a" || a == "--attach" {
			attach = true
			continue
		}
		rest = append(rest, a)
	}
	switch {
	case len(rest) > 0 && (rest[0] == "-h" || rest[0] == "--help"):
		return opts, errors.New("")
	case len(rest) > 0 && (rest[0] == "-o" || rest[0] == "--open"):
		if attach {
			return opts, errors.New("-a only applies to -n (opening a session already attaches)")
		}
		opts.Open = strings.TrimSpace(strings.Join(rest[1:], " "))
		if opts.Open == "" {
			return opts, errors.New("-o needs a session name")
		}
	case len(rest) > 0 && (rest[0] == "-n" || rest[0] == "--new"):
		opts.NewInDir = cwd
		opts.NewName = strings.TrimSpace(strings.Join(rest[1:], " "))
		opts.AttachNew = attach
	case attach:
		return opts, errors.New("-a only applies to -n")
	default:
		// `cav <term>` opens with the session list pre-filtered to <term>.
		opts.Filter = strings.TrimSpace(strings.Join(rest, " "))
	}
	return opts, nil
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, "cav:", err)
		os.Exit(1)
	}
	opts, perr := parseArgs(os.Args[1:], cwd)
	if perr != nil {
		if perr.Error() == "" {
			fmt.Print(usage)
			return
		}
		fmt.Fprintf(os.Stderr, "cav: %s\n%s", perr, usage)
		os.Exit(2)
	}
	m, err := ui.New(opts)
	if err != nil {
		fmt.Fprintln(os.Stderr, "cav:", err)
		os.Exit(1)
	}
	p := tea.NewProgram(m, tea.WithAltScreen())
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "cav:", err)
		os.Exit(1)
	}
}
