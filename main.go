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

func main() {
	// `cav <term>` opens with the session list pre-filtered to <term>.
	term := strings.TrimSpace(strings.Join(os.Args[1:], " "))
	m, err := ui.New(term)
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
