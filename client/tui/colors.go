package tui

import "github.com/gdamore/tcell/v2"

// Every ncli TUI color is defined here as an explicit 24-bit hex value (via
// the ColorIsRGB flag) rather than one of tcell's named ANSI-16 colors
// (ColorPurple, ColorBlue, ...). Those names are indices into the
// terminal's own palette, not fixed colors -- each terminal emulator paints
// them using its own theme, so the same "[blue]" tag renders as a visibly
// different shade of blue depending on whether it's drawn by macOS
// Terminal.app, iTerm2, VS Code's integrated terminal, or a Linux terminal
// emulator. A hex value renders identically on any terminal that supports
// 24-bit color (nearly all modern ones) and degrades consistently on ones
// that don't, instead of depending on which theme happens to be active.
//
// tcell.Color's %s/String() formatting already renders an RGB color with no
// W3C name match as "#RRGGBB", so every constant below can be used both
// directly (SetBorderColor(ColorPrimary)) and inside a dynamic-color tag
// built with fmt.Sprintf("[%s::b]", ColorPrimary).
//
// tcell.ColorDefault (the terminal's own default fg/bg) is deliberately
// left out of this palette and used as-is: unlike the colors below, it's
// supposed to vary with the operator's terminal theme, not be pinned.
const (
	// ColorPrimary is ncli's brand color: borders, selected-row/button
	// backgrounds, title-bar labels and brackets, the logo, JSON object
	// keys, form field backgrounds.
	ColorPrimary tcell.Color = tcell.ColorIsRGB | tcell.ColorValid | 0xA855F7

	// ColorAccent marks something actionable or numeric: keybinding hint
	// keys ("<Tab>"), JSON numeric values, the Debug log level.
	ColorAccent tcell.Color = tcell.ColorIsRGB | tcell.ColorValid | 0x3B82F6

	// ColorMuted de-emphasizes structural text that still needs to stay
	// legible against a widget's black background: timestamps, hint
	// labels, table column headers, the Trace log level.
	ColorMuted tcell.Color = tcell.ColorIsRGB | tcell.ColorValid | 0x9CA3AF

	// ColorText is the primary readable foreground: text on a
	// ColorPrimary-colored background (selected rows, activated buttons),
	// JSON string values, form labels, the Info log level.
	ColorText tcell.Color = tcell.ColorIsRGB | tcell.ColorValid | 0xFFFFFF

	// ColorBadge is a slightly dimmer near-white, reserved for the count
	// inside a title-bar badge (e.g. "PENDING REQUESTS [3]") so it reads
	// as distinct from ColorText's full-white body copy.
	ColorBadge tcell.Color = tcell.ColorIsRGB | tcell.ColorValid | 0xE5E7EB

	// ColorSuccess marks a positive/on/approved state: the Success log
	// level, "On" toggles, approved history entries, inbound flow flags.
	ColorSuccess tcell.Color = tcell.ColorIsRGB | tcell.ColorValid | 0x22C55E

	// ColorDanger marks an error/rejected/lost state: the Error log
	// level, rejected history entries, lost-event counts, urgent
	// (about-to-expire) countdowns.
	ColorDanger tcell.Color = tcell.ColorIsRGB | tcell.ColorValid | 0xEF4444

	// ColorWarning marks a caution state: the Warn log level, expired
	// history entries, soon-to-expire countdowns.
	ColorWarning tcell.Color = tcell.ColorIsRGB | tcell.ColorValid | 0xEAB308
)
