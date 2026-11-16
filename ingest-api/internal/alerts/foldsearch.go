package alerts

import (
	"bytes"
	"regexp/syntax"
	"unicode"
	"unicode/utf8"
)

type foldLiteral struct {
	runes          []rune
	firstRuneOrbit string
}

func newFoldLiteral(text string) foldLiteral {
	runes := []rune(text)
	return foldLiteral{runes: runes, firstRuneOrbit: foldOrbit(runes[0])}
}

func foldOrbit(first rune) string {
	orbit := []rune{first}
	for folded := unicode.SimpleFold(first); folded != first; folded = unicode.SimpleFold(folded) {
		orbit = append(orbit, folded)
	}
	return string(orbit)
}

func foldEqual(left, right rune) bool {
	if left == right {
		return true
	}
	for folded := unicode.SimpleFold(left); folded != left; folded = unicode.SimpleFold(folded) {
		if folded == right {
			return true
		}
	}
	return false
}

func foldLiteralsFromRegex(pattern string) []foldLiteral {
	parsed, err := syntax.Parse(pattern, syntax.Perl)
	if err != nil {
		return nil
	}
	parsed = parsed.Simplify()
	candidates := []*syntax.Regexp{parsed}
	if parsed.Op == syntax.OpAlternate {
		candidates = parsed.Sub
	}
	literals := make([]foldLiteral, 0, len(candidates))
	for _, candidate := range candidates {
		if candidate.Op != syntax.OpLiteral || candidate.Flags&syntax.FoldCase == 0 {
			return nil
		}
		literals = append(literals, newFoldLiteral(string(candidate.Rune)))
	}
	return literals
}

func (literal foldLiteral) matchesAt(text []byte) bool {
	for _, expected := range literal.runes {
		actual, width := utf8.DecodeRune(text)
		if width == 0 || !foldEqual(expected, actual) {
			return false
		}
		text = text[width:]
	}
	return true
}

func (literal foldLiteral) index(text []byte) int {
	position := 0
	for position < len(text) {
		offset := bytes.IndexAny(text[position:], literal.firstRuneOrbit)
		if offset < 0 {
			return -1
		}
		start := position + offset
		if literal.matchesAt(text[start:]) {
			return start
		}
		_, width := utf8.DecodeRune(text[start:])
		position = start + width
	}
	return -1
}

func foldLiteralsIndex(text []byte, literals []foldLiteral) int {
	earliest := -1
	for _, literal := range literals {
		if start := literal.index(text); start >= 0 && (earliest < 0 || start < earliest) {
			earliest = start
		}
	}
	return earliest
}
