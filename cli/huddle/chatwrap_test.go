package huddle

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWrapText_BreaksOnSpaces(t *testing.T) {
	require.Equal(t, []string{"the quick", "brown fox"}, wrapText("the quick brown fox", 10))
}

func TestWrapText_LeavesAShortMessageAlone(t *testing.T) {
	require.Equal(t, []string{"hello"}, wrapText("hello", 40))
}

// A word longer than the pane can never fit, so it is cut rather than left to
// overflow and break the table's alignment.
func TestWrapText_HardBreaksAnOverlongWord(t *testing.T) {
	lines := wrapText(strings.Repeat("x", 25), 10)
	require.Equal(t, []string{"xxxxxxxxxx", "xxxxxxxxxx", "xxxxx"}, lines)
}

func TestWrapText_HardBreaksAnOverlongWordAfterText(t *testing.T) {
	lines := wrapText("hi "+strings.Repeat("x", 12), 10)
	require.Equal(t, []string{"hi", "xxxxxxxxxx", "xx"}, lines)
}

// A pasted multi-line message is not one long line, and a blank line between
// paragraphs is content.
func TestWrapText_HonorsEmbeddedNewlines(t *testing.T) {
	require.Equal(t, []string{"one", "two"}, wrapText("one\ntwo", 40))
	require.Equal(t, []string{"one", "", "two"}, wrapText("one\n\ntwo", 40))
}

// Width is counted in runes: a non-ASCII message would otherwise wrap several
// columns early.
func TestWrapText_CountsRunesNotBytes(t *testing.T) {
	lines := wrapText("ééééé ééééé", 5)
	require.Equal(t, []string{"ééééé", "ééééé"}, lines)
}

// A degenerate width must not loop forever or panic; returning the text
// unwrapped is the only sane answer.
func TestWrapText_SurvivesANonPositiveWidth(t *testing.T) {
	require.Equal(t, []string{"hello"}, wrapText("hello", 0))
	require.Equal(t, []string{"hello"}, wrapText("hello", -4))
}

func TestWrapText_EmptyInput(t *testing.T) {
	require.Equal(t, []string{""}, wrapText("", 10))
}

func TestFirstLine_FlattensAndTruncates(t *testing.T) {
	require.Equal(t, "one two", firstLine("one\ntwo", 40))
	require.Equal(t, "abcde…", firstLine("abcdefghij", 5))
}

func TestShortID_TruncatesLongIDs(t *testing.T) {
	require.Equal(t, "short", shortID("short"))
	require.Equal(t, "aaaaaaaa…", shortID(strings.Repeat("a", 64)))
}
