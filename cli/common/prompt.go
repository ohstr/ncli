package common

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"
)

// PromptYesNo prints prompt and reads a line from stdin, returning true
// only for "y"/"yes" (case-insensitive) -- a read error, EOF, or anything
// else is treated as "no", never as "yes".
func PromptYesNo(r *bufio.Reader, prompt string) bool {
	fmt.Print(prompt)
	line, _ := r.ReadString('\n')
	line = strings.ToLower(strings.TrimSpace(line))
	return line == "y" || line == "yes"
}

// PromptLine prints prompt and reads a line from stdin, trimming its
// trailing newline.
func PromptLine(r *bufio.Reader, prompt string) (string, error) {
	fmt.Print(prompt)
	line, err := r.ReadString('\n')
	if err != nil && !errors.Is(err, os.ErrClosed) && line == "" {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}
