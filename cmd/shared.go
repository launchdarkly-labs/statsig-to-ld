package cmd

import (
	"fmt"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"
)

// promptForKey prompts the user to enter an API key with echo disabled,
// so the key does not appear in terminal output or scrollback.
func promptForKey(label string) (string, error) {
	if !term.IsTerminal(int(syscall.Stdin)) {
		// Non-interactive (piped input, CI) — cannot prompt
		return "", nil
	}
	fmt.Fprintf(os.Stderr, "Enter %s: ", label)
	keyBytes, err := term.ReadPassword(int(syscall.Stdin))
	fmt.Fprintln(os.Stderr) // newline after hidden input
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(keyBytes)), nil
}

func parseCommaSeparated(s string) []string {
	if s == "" {
		return nil
	}
	parts := strings.Split(s, ",")
	var result []string
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			result = append(result, p)
		}
	}
	return result
}
