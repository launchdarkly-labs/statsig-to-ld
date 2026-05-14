// Package output provides colored terminal output helpers for interactive
// migration workflows.
package output

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"

	"golang.org/x/term"
)

var noColor bool

func init() {
	// Auto-disable color when stderr is not a terminal (e.g. piped output).
	if !term.IsTerminal(int(os.Stderr.Fd())) {
		noColor = true
	}
}

// SetNoColor explicitly enables or disables color output.
func SetNoColor(disabled bool) { noColor = disabled }

const (
	ansiBold   = "\033[1m"
	ansiDim    = "\033[2m"
	ansiGreen  = "\033[92m"
	ansiYellow = "\033[93m"
	ansiRed    = "\033[91m"
	ansiCyan   = "\033[96m"
	ansiReset  = "\033[0m"
)

// C returns the ANSI code, or "" when color is disabled.
func C(code string) string {
	if noColor {
		return ""
	}
	return code
}

// Banner prints the tool banner.
func Banner() {
	fmt.Fprintf(os.Stderr, "\n%s%s%s\n", C(ansiBold+ansiCyan), "============================================================", C(ansiReset))
	fmt.Fprintln(os.Stderr, "  Statsig -> LaunchDarkly Warehouse Native Migration Tool")
	fmt.Fprintf(os.Stderr, "%s%s\n\n", "============================================================", C(ansiReset))
}

// Phase prints a phase header.
func Phase(n int, title string) {
	fmt.Fprintf(os.Stderr, "\n%s[Phase %d]%s %s\n", C(ansiBold), n, C(ansiReset), title)
}

// Ok prints a success message.
func Ok(msg string) {
	fmt.Fprintf(os.Stderr, "  %sOK%s %s\n", C(ansiGreen), C(ansiReset), msg)
}

// Warn prints a warning message.
func Warn(msg string) {
	fmt.Fprintf(os.Stderr, "  %sWARN%s %s\n", C(ansiYellow), C(ansiReset), msg)
}

// ErrMsg prints an error message.
func ErrMsg(msg string) {
	fmt.Fprintf(os.Stderr, "  %sERR%s %s\n", C(ansiRed), C(ansiReset), msg)
}

// Info prints an informational message.
func Info(msg string) {
	fmt.Fprintf(os.Stderr, "  %s\n", msg)
}

// Progress prints an in-progress creation line (no newline).
func Progress(index, total int, name, detail string) {
	suffix := ""
	if detail != "" {
		suffix = fmt.Sprintf(" (%s)", detail)
	}
	fmt.Fprintf(os.Stderr, "  [%d/%d] Creating \"%s\"%s... ", index, total, name, suffix)
}

// Done prints a green "done" marker.
func Done() {
	fmt.Fprintf(os.Stderr, "%sdone%s\n", C(ansiGreen), C(ansiReset))
}

// Skip prints a yellow "skipped" marker with an optional reason.
func Skip(reason string) {
	msg := "skipped"
	if reason != "" {
		msg = fmt.Sprintf("skipped (%s)", reason)
	}
	fmt.Fprintf(os.Stderr, "%s%s%s\n", C(ansiYellow), msg, C(ansiReset))
}

// Fail prints a red "FAILED" marker with an optional reason.
func Fail(reason string) {
	msg := "FAILED"
	if reason != "" {
		msg = fmt.Sprintf("FAILED (%s)", reason)
	}
	fmt.Fprintf(os.Stderr, "%s%s%s\n", C(ansiRed), msg, C(ansiReset))
}

// ShowScript displays a SQL setup script in the terminal and copies it to the clipboard.
func ShowScript(script string) {
	if script == "" {
		return
	}
	fmt.Fprintln(os.Stderr)
	fmt.Fprintf(os.Stderr, "  %s%s\n", C(ansiCyan), strings.Repeat("=", 56))
	fmt.Fprintln(os.Stderr, "  Run the following SQL in your warehouse as admin")
	fmt.Fprintf(os.Stderr, "  %s%s\n", strings.Repeat("=", 56), C(ansiReset))
	for _, line := range strings.Split(strings.TrimSpace(script), "\n") {
		fmt.Fprintf(os.Stderr, "  %s%s%s\n", C(ansiDim), line, C(ansiReset))
	}
	fmt.Fprintf(os.Stderr, "  %s%s%s\n", C(ansiCyan), strings.Repeat("=", 56), C(ansiReset))

	if CopyToClipboard(script) {
		Ok("Script copied to clipboard")
	}
}

// CopyToClipboard copies text to the system clipboard. Returns true on success.
func CopyToClipboard(text string) bool {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("pbcopy")
	case "linux":
		cmd = exec.Command("xclip", "-selection", "clipboard")
	default:
		return false
	}
	cmd.Stdin = strings.NewReader(text)
	return cmd.Run() == nil
}
