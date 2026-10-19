package cmd

import (
	"fmt"
	"log"
	"os"
	"strings"
	"syscall"

	"golang.org/x/term"

	"github.com/launchdarkly-labs/statsig-to-ld/internal/launchdarkly"
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

// logMaintainer reports which member will own what the run creates. noun is
// singular, e.g. "metric"; except qualifies "every <noun> created by this run".
func logMaintainer(m launchdarkly.Maintainer, noun, except string) {
	if m.OptedOut {
		log.Printf("Maintainer: NONE. No maintainer is sent with any %s this run creates%s. With a personal token, "+
			"LaunchDarkly makes the token's member its maintainer; otherwise its UI flags it as incomplete until someone is assigned.", noun, except)
		return
	}
	who := m.MemberID
	if m.Email != "" {
		who = m.Email
	}
	log.Printf("Maintainer: %s (%s). Assigned to every %s created by this run%s.", who, m.Source, noun, except)
}
