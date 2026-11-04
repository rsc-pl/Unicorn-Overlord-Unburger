package importer

import (
	"sort"
	"strings"
)

// Control-token tokenizer
// -----------------------
// Message text embeds runtime control commands introduced by '#' or '@'. The
// tokenizer recognises, at each introducer:
//
//   - "#/name" and "@/name" closing commands (e.g. "#/i", "#/c")
//   - "#name(...)" and "@name(...)" commands with a parenthesised argument,
//     including the "##(...)" and "#c(...)" styles (e.g. "#w(20)", "#c(12)")
//   - "#i" as the italic-open marker, recognised before the generic rule so
//     that "#iword" yields the italic token "#i" rather than a bogus
//     "#iword"
//   - any other "#name" / "@name" bare command (e.g. "#s", "#FN", "@w")
//
// It is intentionally a lexical extractor: it makes no attempt to validate
// command names or arguments, and never invents Japanese or English text.

// Tokenize returns the control tokens found in s, in order. The result is
// deterministic for a given input.
func Tokenize(s string) []string {
	var toks []string
	i, n := 0, len(s)
	for i < n {
		switch s[i] {
		case '#':
			switch {
			case i+2 < n && s[i+1] == '#' && s[i+2] == '(':
				j := indexClose(s, i+3)
				toks = append(toks, s[i:j])
				i = j
			case i+1 < n && s[i+1] == '/':
				j := scanAlpha(s, i+2)
				toks = append(toks, s[i:j])
				i = j
			case i+1 < n && s[i+1] == 'i':
				toks = append(toks, "#i")
				i += 2
			case i+1 < n && isLetter(s[i+1]):
				j := scanAlpha(s, i+1)
				if j < n && s[j] == '(' {
					j = indexClose(s, j+1)
				}
				toks = append(toks, s[i:j])
				i = j
			default:
				i++
			}
		case '@':
			switch {
			case i+1 < n && s[i+1] == '/':
				j := scanAlpha(s, i+2)
				toks = append(toks, s[i:j])
				i = j
			case i+1 < n && isLetter(s[i+1]):
				j := scanAlpha(s, i+1)
				if j < n && s[j] == '(' {
					j = indexClose(s, j+1)
				}
				toks = append(toks, s[i:j])
				i = j
			default:
				i++
			}
		default:
			i++
		}
	}
	return toks
}

// StripTokens returns s with every control token removed, preserving all other
// bytes verbatim. It walks the same lexical rules as Tokenize and never invents
// text. The result is what a reader would see if the engine ignored commands.
func StripTokens(s string) string {
	var b []byte
	i, n := 0, len(s)
	for i < n {
		switch s[i] {
		case '#':
			switch {
			case i+2 < n && s[i+1] == '#' && s[i+2] == '(':
				i = indexClose(s, i+3)
			case i+1 < n && s[i+1] == '/':
				i = scanAlpha(s, i+2)
			case i+1 < n && s[i+1] == 'i':
				i += 2
			case i+1 < n && isLetter(s[i+1]):
				j := scanAlpha(s, i+1)
				if j < n && s[j] == '(' {
					j = indexClose(s, j+1)
				}
				i = j
			default:
				b = append(b, s[i])
				i++
			}
		case '@':
			switch {
			case i+1 < n && s[i+1] == '/':
				i = scanAlpha(s, i+2)
			case i+1 < n && isLetter(s[i+1]):
				j := scanAlpha(s, i+1)
				if j < n && s[j] == '(' {
					j = indexClose(s, j+1)
				}
				i = j
			default:
				b = append(b, s[i])
				i++
			}
		default:
			b = append(b, s[i])
			i++
		}
	}
	return string(b)
}

// VisibleRunes counts the runes a reader sees: control tokens removed and hard
// line breaks dropped.
func VisibleRunes(s string) int {
	stripped := StripTokens(s)
	n := 0
	for _, r := range stripped {
		if r == '\n' || r == '\r' {
			continue
		}
		n++
	}
	return n
}

// AuthoredLines counts the authored hard-break segments (newline separators + 1)
// after control tokens are removed. An empty string has one segment.
func AuthoredLines(s string) int {
	return strings.Count(StripTokens(s), "\n") + 1
}

// MaxAuthoredSegment returns the number of visible runes in the longest
// authored hard-break segment (control tokens removed, newlines excluded).
func MaxAuthoredSegment(s string) int {
	max, cur := 0, 0
	for _, r := range StripTokens(s) {
		switch r {
		case '\n', '\r':
			if cur > max {
				max = cur
			}
			cur = 0
		default:
			cur++
		}
	}
	if cur > max {
		max = cur
	}
	return max
}

// ItalicOnlyDiff reports whether every differing token is an italic marker.
func ItalicOnlyDiff(diff map[string]int) bool { return italicOnlyDiff(diff) }

// indexClose returns the index just past the closing ')' starting the search
// at from, or len(s) when no ')' is found.
func indexClose(s string, from int) int {
	for j := from; j < len(s); j++ {
		if s[j] == ')' {
			return j + 1
		}
	}
	return len(s)
}

// scanAlpha returns the index of the first non-letter at or after from.
func scanAlpha(s string, from int) int {
	j := from
	for j < len(s) && isLetter(s[j]) {
		j++
	}
	return j
}

func isLetter(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z')
}

// TokenDiff returns the multiset difference between two token streams as a
// token -> signed-count map. Positive counts are tokens present only (or more
// often) in a; negative counts are tokens present only (or more often) in b.
// The map is empty when the multisets are equal.
func TokenDiff(a, b []string) map[string]int {
	counts := map[string]int{}
	for _, t := range a {
		counts[t]++
	}
	for _, t := range b {
		counts[t]--
	}
	for t, c := range counts {
		if c == 0 {
			delete(counts, t)
		}
	}
	return counts
}

// diffTokens returns the differing tokens of a TokenDiff in sorted order.
func diffTokens(diff map[string]int) []string {
	out := make([]string, 0, len(diff))
	for t := range diff {
		out = append(out, t)
	}
	sort.Strings(out)
	return out
}

// italicTokens are the only tokens whose difference against the official
// English may be waived by the markup-exception allowlist.
var italicTokens = map[string]bool{"#i": true, "#/i": true}

// italicOnlyDiff reports whether every differing token is an italic marker.
func italicOnlyDiff(diff map[string]int) bool {
	if len(diff) == 0 {
		return false
	}
	for t := range diff {
		if !italicTokens[t] {
			return false
		}
	}
	return true
}
