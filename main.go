// Command cav is a custom Claude Code agents view: a terminal UI to list,
// create, delete, reorder, search, and open background Claude sessions.
package main

import (
	"fmt"
	"os"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/omeryahud/cav/internal/ui"
)

const usage = `usage: cav [term]            open with the list filtered to <term>
       cav -o <name>        open the session named <name> (--open)
`

func main() {
	args := os.Args[1:]
	var filter, open string
	switch {
	case len(args) > 0 && (args[0] == "-h" || args[0] == "--help"):
		fmt.Print(usage)
		return
	case len(args) > 0 && (args[0] == "-o" || args[0] == "--open"):
		// Everything after the flag is the session name (names can have spaces).
		open = strings.TrimSpace(strings.Join(args[1:], " "))
		if open == "" {
			fmt.Fprint(os.Stderr, usage)
			os.Exit(2)
		}
	default:
		// `cav <term>` opens with the session list pre-filtered to <term>.
		filter = strings.TrimSpace(strings.Join(args, " "))
	}
	m, err := ui.New(filter, open)
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
