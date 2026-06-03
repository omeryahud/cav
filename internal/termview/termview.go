// Package termview reconstructs a terminal screen from a raw byte stream — e.g.
// the cursor-addressed redraw sequences that `claude logs` emits — and renders
// it as styled ANSI lines sized to a target width×height. It's how cav shows a
// live snapshot of a running session in the preview pane, instead of plain text.
package termview

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/hinshun/vt10x"
)

// vt10x glyph attribute bits. They're unexported in vt10x; the values are
// 1<<iota in the source order (reverse, underline, bold, italic, blink), so we
// mirror them here to read Glyph.Mode.
const (
	attrReverse   int16 = 1 << 0
	attrUnderline int16 = 1 << 1
	attrBold      int16 = 1 << 2
	attrItalic    int16 = 1 << 3
)

const styleMask = attrReverse | attrUnderline | attrBold | attrItalic

// maxBytes caps how much trailing output we emulate, to bound parse time on a
// large dump. The stream is full-screen redraws, so a tail still converges to
// the current screen.
const maxBytes = 256 << 10

// Render reconstructs the session screen and returns it as styled ANSI lines,
// line-wrapped to wrapWidth so no horizontal content is lost.
//
// The stream is emulated at emuCols×emuRows — which must be >= the session's own
// terminal size, or its pre-wrapped lines re-wrap and garble. Each non-empty row
// is then re-flowed into wrapWidth-wide segments. The result can be taller than
// the pane; the caller bottom-anchors and clips it to the latest content.
func Render(raw []byte, emuCols, emuRows, wrapWidth int) string {
	emuCols, emuRows, wrapWidth = max(emuCols, 1), max(emuRows, 1), max(wrapWidth, 1)
	if len(raw) > maxBytes {
		raw = raw[len(raw)-maxBytes:]
	}
	term := vt10x.New(vt10x.WithSize(emuCols, emuRows))
	_, _ = term.Write(raw)
	term.Lock()
	defer term.Unlock()
	cols, rows := term.Size()
	content := lastContentRow(term, cols, rows)
	var out []string
	for y := 0; y < content; y++ {
		out = append(out, wrapRow(term, y, cols, wrapWidth)...)
	}
	return strings.Join(out, "\n")
}

// wrapRow renders row y (trailing blanks trimmed) re-flowed into wrapWidth-wide
// segments, preserving per-cell styling. A blank row yields one empty line so
// vertical spacing is kept.
func wrapRow(term vt10x.View, y, cols, wrapWidth int) []string {
	end := cols
	for end > 0 && blank(term.Cell(end-1, y)) {
		end--
	}
	if end == 0 {
		return []string{""}
	}
	var lines []string
	for x := 0; x < end; x += wrapWidth {
		lines = append(lines, renderSegment(term, y, x, min(x+wrapWidth, end)))
	}
	return lines
}

// renderSegment renders cells [x0,x1) of row y, coalescing runs of same-style
// cells into one styled segment so the output stays compact.
func renderSegment(term vt10x.View, y, x0, x1 int) string {
	var out strings.Builder
	for x := x0; x < x1; {
		start := term.Cell(x, y)
		var run []rune
		for x < x1 {
			g := term.Cell(x, y)
			if !sameStyle(g, start) {
				break
			}
			ch := g.Char
			if ch < 32 { // empty cell (0) or stray control char
				ch = ' '
			}
			run = append(run, ch)
			x++
		}
		out.WriteString(styleFor(start).Render(string(run)))
	}
	return out.String()
}

// lastContentRow returns one past the last row with any visible content, so the
// trailing blank rows of an over-tall emulator aren't emitted.
func lastContentRow(term vt10x.View, cols, rows int) int {
	for y := rows - 1; y >= 0; y-- {
		for x := 0; x < cols; x++ {
			if !blank(term.Cell(x, y)) {
				return y + 1
			}
		}
	}
	return 0
}

// blank reports whether a cell is empty space on the default background (so it
// can be trimmed); a space carrying a background color is kept.
func blank(g vt10x.Glyph) bool {
	return (g.Char == 0 || g.Char == ' ') && g.Mode&attrReverse == 0 && g.BG >= 1<<24
}

func sameStyle(a, b vt10x.Glyph) bool {
	return a.FG == b.FG && a.BG == b.BG && (a.Mode&styleMask) == (b.Mode&styleMask)
}

func styleFor(g vt10x.Glyph) lipgloss.Style {
	fg, bg := g.FG, g.BG
	if g.Mode&attrReverse != 0 { // vt10x leaves the swap to the renderer
		fg, bg = bg, fg
	}
	st := lipgloss.NewStyle()
	if c, ok := vtColor(fg); ok {
		st = st.Foreground(c)
	}
	if c, ok := vtColor(bg); ok {
		st = st.Background(c)
	}
	if g.Mode&attrBold != 0 {
		st = st.Bold(true)
	}
	if g.Mode&attrUnderline != 0 {
		st = st.Underline(true)
	}
	if g.Mode&attrItalic != 0 {
		st = st.Italic(true)
	}
	return st
}

// vtColor maps a vt10x color to a lipgloss color. ok is false for the terminal
// default (so the caller leaves fg/bg unset).
func vtColor(c vt10x.Color) (lipgloss.Color, bool) {
	switch {
	case c >= 1<<24: // DefaultFG / DefaultBG / DefaultCursor
		return "", false
	case c < 256: // 16- or 256-color palette index
		return lipgloss.Color(strconv.Itoa(int(c))), true
	default: // 24-bit RGB packed in the low 24 bits
		return lipgloss.Color(fmt.Sprintf("#%06x", uint32(c)&0xffffff)), true
	}
}
