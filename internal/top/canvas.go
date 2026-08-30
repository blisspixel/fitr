package top

import (
	"strings"
	"unicode"

	"github.com/blisspixel/fitr/internal/render"
)

// Role is a semantic visual role. Terminal colors are selected only by the
// adapter, so state and layout remain usable by future native surfaces.
type Role uint8

const (
	RoleDefault Role = iota
	RoleMuted
	RoleHeader
	RoleAccent
	RolePass
	RoleFail
	RoleWarning
	RoleSelected
)

// Color is a small portable palette token, not a terminal color value.
type Color uint8

const (
	ColorDefault Color = iota
	ColorRed
	ColorGreen
	ColorYellow
	ColorBlue
	ColorMagenta
	ColorCyan
	ColorGray
)

// Style describes one semantic role.
type Style struct {
	Foreground Color
	Bold, Dim  bool
	Reverse    bool
}

// Theme maps semantic roles to portable styles.
type Theme struct {
	Styles [RoleSelected + 1]Style
}

// DefaultTheme uses the terminal default background. In no-color mode only
// text attributes remain, preserving contrast in user-selected terminal
// themes.
func DefaultTheme(noColor bool) Theme {
	var theme Theme
	theme.Styles[RoleMuted] = Style{Foreground: ColorGray, Dim: true}
	theme.Styles[RoleHeader] = Style{Foreground: ColorBlue, Bold: true}
	theme.Styles[RoleAccent] = Style{Foreground: ColorMagenta, Bold: true}
	theme.Styles[RolePass] = Style{Foreground: ColorGreen}
	theme.Styles[RoleFail] = Style{Foreground: ColorRed, Bold: true}
	theme.Styles[RoleWarning] = Style{Foreground: ColorYellow, Bold: true}
	theme.Styles[RoleSelected] = Style{Bold: true, Reverse: true}
	if noColor {
		for i := range theme.Styles {
			theme.Styles[i].Foreground = ColorDefault
		}
	}
	return theme
}

// Glyphs contains every character with an ASCII fallback.
type Glyphs struct {
	Horizontal, Vertical, Selected string
	Full, Empty                    string
	Spark                          []rune
	Ellipsis, Dot                  string
}

// DefaultGlyphs returns either Unicode data glyphs or stable ASCII fallbacks.
//
// Spark is empty on the ASCII side on purpose. `.:-=+*#@` is not a perceptual
// ramp: `#` and `@` are dense but not tall, `-` sits mid-cell, and a reader
// cannot order those by height, which is the only channel a sparkline has.
// Structure and bars degrade to ASCII fine; a shape does not, so it is refused
// rather than drawn in glyphs nobody can read.
func DefaultGlyphs(ascii bool) Glyphs {
	if ascii {
		return Glyphs{Horizontal: "-", Vertical: "|", Selected: ">", Full: "#",
			Empty: ".", Ellipsis: "...", Dot: " | "}
	}
	return Glyphs{Horizontal: "─", Vertical: "│", Selected: "›", Full: "█",
		Empty: "░", Spark: []rune("▁▂▃▄▅▆▇█"), Ellipsis: "…", Dot: " · "}
}

// Span is one styled segment of a canvas row.
type Span struct {
	Text string
	Role Role
}

// Canvas is a clipped, renderer-neutral list of styled rows.
type Canvas struct {
	Width, Height int
	Rows          [][]Span
}

// NewCanvas allocates an empty canvas.
func NewCanvas(width, height int) Canvas {
	width, height = max(width, 1), max(height, 1)
	return Canvas{Width: width, Height: height, Rows: make([][]Span, height)}
}

// SetLine replaces one row, sanitizing and clipping every span.
func (c *Canvas) SetLine(y int, spans ...Span) {
	if y < 0 || y >= c.Height {
		return
	}
	remaining := c.Width
	line := make([]Span, 0, len(spans))
	for _, span := range spans {
		if remaining <= 0 {
			break
		}
		text := singleLine(span.Text)
		text = clipCells(text, remaining, "...")
		if text == "" {
			continue
		}
		line = append(line, Span{Text: text, Role: span.Role})
		remaining -= displayWidth(text)
	}
	c.Rows[y] = line
}

// Plain returns the visible text without style or trailing whitespace.
func (c Canvas) Plain() string {
	lines := make([]string, c.Height)
	for y, row := range c.Rows {
		var b strings.Builder
		for _, span := range row {
			b.WriteString(span.Text)
		}
		lines[y] = strings.TrimRight(b.String(), " ")
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// Sanitize removes terminal control sequences and C0/C1 control characters.
// Model and runtime strings are untrusted terminal input.
func Sanitize(value string) string {
	return render.SingleLine(value)
}

func singleLine(value string) string {
	leading := len(value) - len(strings.TrimLeft(value, " "))
	trailing := len(value) - len(strings.TrimRight(value, " "))
	if leading+trailing > len(value) {
		trailing = len(value) - leading
	}
	coreEnd := len(value) - trailing
	core := ""
	if leading < coreEnd {
		core = Sanitize(value[leading:coreEnd])
	}
	return strings.Repeat(" ", leading) + core + strings.Repeat(" ", trailing)
}

func clipCells(value string, width int, ellipsis string) string {
	if width <= 0 {
		return ""
	}
	if displayWidth(value) <= width {
		return value
	}
	ellWidth := displayWidth(ellipsis)
	if ellWidth > width {
		ellipsis = ""
		ellWidth = 0
	}
	limit := width - ellWidth
	var out strings.Builder
	used := 0
	for _, r := range value {
		rw := runeWidth(r)
		if used+rw > limit {
			break
		}
		out.WriteRune(r)
		used += rw
	}
	return strings.TrimRight(out.String(), " ") + ellipsis
}

func padCells(value string, width int) string {
	value = clipCells(value, width, "")
	return value + strings.Repeat(" ", max(width-displayWidth(value), 0))
}

func displayWidth(value string) int {
	width := 0
	for _, r := range value {
		width += runeWidth(r)
	}
	return width
}

func runeWidth(r rune) int {
	if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Me, r) || r == 0x200d {
		return 0
	}
	if r >= 0x1100 && (r <= 0x115f || r == 0x2329 || r == 0x232a ||
		(r >= 0x2e80 && r <= 0xa4cf) || (r >= 0xac00 && r <= 0xd7a3) ||
		(r >= 0xf900 && r <= 0xfaff) || (r >= 0xfe10 && r <= 0xfe6f) ||
		(r >= 0xff00 && r <= 0xff60) || (r >= 0xffe0 && r <= 0xffe6) ||
		(r >= 0x1f300 && r <= 0x1faff) || (r >= 0x20000 && r <= 0x3fffd)) {
		return 2
	}
	return 1
}
