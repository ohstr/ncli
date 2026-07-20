package tui

import (
	"fmt"
	"strings"

	"github.com/rivo/tview"
)

const (
	LOGO = `
  _
-(')
  ;;
 //
 `
	WELCOME_MESSAGE = "Take your ownership back"
)

// alignedLogo pads each line of LOGO's ASCII art to the same width.
//
// tview's TextView centers multi-line text one line at a time, based on
// that line's own width (see textview.go's AlignCenter handling). When the
// art's lines have differing widths, integer-division rounding shifts odd-
// and even-width lines by a column relative to each other depending on the
// container's width parity, visibly misaligning the art. Padding every
// line to the same width makes every line get an identical offset.
func alignedLogo() string {
	lines := strings.Split(LOGO, "\n")
	if len(lines) < 3 {
		return LOGO
	}

	width := 0
	for _, line := range lines[1 : len(lines)-1] {
		if w := len([]rune(line)); w > width {
			width = w
		}
	}

	for i := 1; i < len(lines)-1; i++ {
		if pad := width - len([]rune(lines[i])); pad > 0 {
			lines[i] += strings.Repeat(" ", pad)
		}
	}

	return strings.Join(lines, "\n")
}

type Header struct {
	*tview.Flex
	Logo *tview.TextView
}

func NewHeader() *Header {
	h := &Header{
		Flex: tview.NewFlex(),
		Logo: tview.NewTextView(),
	}

	h.drawLogo()

	return h
}

func (h *Header) drawLogo() {
	h.Logo.SetDynamicColors(true)

	lines := strings.Split(LOGO, "\n")
	fmt.Fprint(h.Logo, "[purple]")
	for i := 1; i < len(lines)-1; i++ {
		fmt.Fprintf(h.Logo, "   [%s::b]%s", "", lines[i])
		fmt.Fprintf(h.Logo, "\n")
	}

	h.AddItem(h.Logo, 0, 1, false)
}
