package banner

import (
	"fmt"
	"io"
	"os"
	"strings"
)

// The art is deliberately 4 lines, not 6. A banner that fills half the
// terminal on every invocation stops being charming after the third run.
//
// Generated with: figlet -f smslant "AKPA"
const art = `   ___   __ _____  ___
  / _ | / //_/ _ \/ _ |
 / __ |/ ,< / ___/ __ |
/_/ |_/_/|_/_/  /_/ |_|`

// ANSI escapes, written out rather than pulled from a dependency. A banner
// is not worth a module in your go.mod.
const (
	reset  = "\033[0m"
	bold   = "\033[1m"
	dim    = "\033[2m"
	amber  = "\033[38;5;214m"
	green  = "\033[32m"
	redcol = "\033[31m"
)

// colorEnabled reports whether we should emit escape codes.
//
// Three things can turn color off, and all three matter:
//   - stdout is not a terminal (piped to a file, or into `less`, or captured
//     by a script) — escape codes there are garbage in someone's output
//   - NO_COLOR is set, per the no-color.org convention
//   - TERM=dumb, which some CI runners and editors set
func colorEnabled(w io.Writer) bool {
	if os.Getenv("NO_COLOR") != "" {
		return false
	}
	if os.Getenv("TERM") == "dumb" {
		return false
	}

	f, ok := w.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	// ModeCharDevice means it's a terminal rather than a regular file or pipe.
	return info.Mode()&os.ModeCharDevice != 0
}

type palette struct {
	reset, bold, dim, accent, ok, bad string
}

func paletteFor(w io.Writer) palette {
	if !colorEnabled(w) {
		return palette{}
	}
	return palette{reset: reset, bold: bold, dim: dim, accent: amber, ok: green, bad: redcol}
}

// Print writes the startup banner.
//
// It goes to stderr, not stdout. That is deliberate: if someone runs
// `akpa --print-link > link.txt`, the banner must not end up in the file.
// Decoration goes to stderr; data goes to stdout.
func Print(version string) {
	w := os.Stderr
	p := paletteFor(w)

	fmt.Fprintln(w)
	for _, line := range strings.Split(art, "\n") {
		fmt.Fprintf(w, "  %s%s%s%s\n", p.bold, p.accent, line, p.reset)
	}
	fmt.Fprintf(w, "  %sshare a folder over one link  ·  %s%s\n\n", p.dim, version, p.reset)
}

// Step prints a line describing something in progress, with no newline
// terminator, so Done() or Fail() can complete it on the same line.
//
// The point of these is that a CLI which prints nothing while it works looks
// identical to a CLI that has crashed. Every operation that can block for more
// than a moment should announce itself before it starts, not after it finishes.
func Step(w io.Writer, format string, args ...any) {
	p := paletteFor(w)
	fmt.Fprintf(w, "  %s…%s %s", p.dim, p.reset, fmt.Sprintf(format, args...))
}

// Done completes a Step line with a checkmark.
func Done(w io.Writer, detail string) {
	p := paletteFor(w)
	if detail != "" {
		fmt.Fprintf(w, "  %s%s%s", p.dim, detail, p.reset)
	}
	fmt.Fprintf(w, " %s✓%s\n", p.ok, p.reset)
}

// Fail completes a Step line with a cross.
func Fail(w io.Writer) {
	p := paletteFor(w)
	fmt.Fprintf(w, " %s✗%s\n", p.bad, p.reset)
}

// Hint prints indented follow-up guidance under an error.
func Hint(format string, args ...any) {
	p := paletteFor(os.Stderr)
	fmt.Fprintf(os.Stderr, "         %s%s%s\n", p.dim, fmt.Sprintf(format, args...), p.reset)
}

// Ready prints the few lines that actually matter once the tunnel is live.
//
// The password is never echoed — the user already knows it, and a terminal
// scrollback is exactly the place it should not end up.
func Ready(w io.Writer, dir, link string, protected bool) {
	p := paletteFor(w)

	fmt.Fprintf(w, "  %sserving%s  %s\n", p.dim, p.reset, dir)
	if protected {
		fmt.Fprintf(w, "  %slocked%s   password required\n", p.dim, p.reset)
	}
	fmt.Fprintf(w, "  %slink%s     %s%s%s%s\n\n", p.dim, p.reset, p.bold, p.accent, link, p.reset)
	fmt.Fprintf(w, "  %sready. ctrl-c to stop.%s\n\n", p.dim, p.reset)
}

// Request logs one proxied request. Keep it to a single line — a tunnel that
// scrolls three lines per asset is unreadable when a page loads 40 of them.
func Request(w io.Writer, method, path string, status int, dur string) {
	p := paletteFor(w)

	color := p.ok
	if status >= 400 {
		color = p.bad
	}
	fmt.Fprintf(w, "  %s%d%s  %-6s %-40s %s%s%s\n",
		color, status, p.reset, method, truncate(path, 40), p.dim, dur, p.reset)
}

// Errorf prints a user-facing error. Not a panic — a stack trace tells the
// user nothing they can act on.
func Errorf(format string, args ...any) {
	p := paletteFor(os.Stderr)
	fmt.Fprintf(os.Stderr, "\n  %serror%s  %s\n\n", p.bad, p.reset, fmt.Sprintf(format, args...))
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}
