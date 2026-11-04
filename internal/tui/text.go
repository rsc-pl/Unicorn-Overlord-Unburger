package tui

import (
	"strings"
)

// This file holds the small text-layout helpers shared by every screen. They are
// rune-aware: a terminal column is a rune here, so box-drawing characters and
// ellipses never split a multi-byte sequence, and width arithmetic stays exact
// for the printable text the interface emits.

// truncate shortens a single-line string to width rune columns, appending an
// ellipsis. A width of one column cannot carry an ellipsis, so the first rune is
// kept: the compact fallback still prints something instead of a blank screen.
func truncate(s string, width int) string {
	if width <= 0 {
		return ""
	}
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= width {
		return s
	}
	if width == 1 {
		return string(r[:1])
	}
	return string(r[:width-1]) + "…"
}

// wrap hard-wraps s to at most width columns per line, breaking on spaces where
// possible and mid-word otherwise. Unlike truncate it never drops content, so a
// diagnostic message stays readable on a very narrow terminal.
func wrap(s string, width int) []string {
	if width <= 0 {
		return []string{s}
	}
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(s, "\r", ""), "\n") {
		words := strings.Fields(para)
		if len(words) == 0 {
			out = append(out, "")
			continue
		}
		line := ""
		for _, word := range words {
			r := []rune(word)
			for len(r) > width {
				if line != "" {
					out = append(out, line)
					line = ""
				}
				out = append(out, string(r[:width]))
				r = r[width:]
			}
			word = string(r)
			switch {
			case line == "":
				line = word
			case len([]rune(line))+1+len(r) <= width:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		out = append(out, line)
	}
	return out
}

// fitWidth pads or truncates every line to at most width columns.
func fitWidth(s string, width int) string {
	if width <= 0 {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = truncate(ln, width)
	}
	return strings.Join(lines, "\n")
}

// padTo right-pads s with spaces to exactly width rune columns, truncating when
// it is longer. It is the primitive every table cell and panel line uses, so the
// right-hand border always lines up.
func padTo(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) > width {
		return truncate(s, width)
	}
	if len(r) == width {
		return string(r)
	}
	return string(r) + strings.Repeat(" ", width-len(r))
}

// padLeft left-pads s with spaces to exactly width rune columns (numbers and
// sizes read better right-aligned).
func padLeft(s string, width int) string {
	if width <= 0 {
		return ""
	}
	r := []rune(strings.ReplaceAll(s, "\n", " "))
	if len(r) > width {
		return truncate(s, width)
	}
	if len(r) == width {
		return string(r)
	}
	return strings.Repeat(" ", width-len(r)) + string(r)
}

// runeLen reports the printable width of s in terminal columns.
func runeLen(s string) int { return len([]rune(s)) }

// indent prefixes every line of s.
func indent(s, prefix string) string {
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = prefix + ln
	}
	return strings.Join(lines, "\n")
}
