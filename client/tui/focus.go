package tui

import "github.com/rivo/tview"

// focusNotifier is satisfied by any primitive embedding *tview.Box (every
// Table/TextView/Flex in this codebase does), letting WireFocusBorder
// accept the concrete leaf types callers already have instead of forcing
// them through tview.Primitive, which doesn't expose these promoted
// methods.
type focusNotifier interface {
	SetFocusFunc(func()) *tview.Box
	SetBlurFunc(func()) *tview.Box
}

// WireFocusBorder makes border's color track whether target currently
// holds real keyboard focus: ColorPrimary while it does, ColorMuted while
// it doesn't. target and border are the same underlying primitive's own
// *Box for a plain Table/EventTable (e.g. WireFocusBorder(t.Table,
// t.Table.Box)), but differ for a bordered Flex wrapping its own
// focusable leaf (e.g. cli/bunker's PendingTable, this package's own
// Logger): target is whatever FocusTarget() returns, border is the outer
// Flex's own Box, actually drawing the border.
//
// Every board here keeps several bordered panels on screen at once, each
// previously hardcoding the same SetBorderColor(ColorPrimary) regardless
// of which one Tab/arrow keys would actually move next -- from a glance,
// every panel's border looked equally "active", an ambiguity tview's own
// heavier focused-border glyph (App's init() swaps in Borders.*Focus) is
// too subtle to resolve on its own with several such panels visible at
// once (see cli/bunker's board: Pending/Trusted Apps/Request History/the
// log pane, all rendered simultaneously). Dimming every non-focused
// panel's border resolves it unambiguously.
func WireFocusBorder(target focusNotifier, border *tview.Box) {
	border.SetBorderColor(ColorMuted)
	target.SetFocusFunc(func() { border.SetBorderColor(ColorPrimary) })
	target.SetBlurFunc(func() { border.SetBorderColor(ColorMuted) })
}
