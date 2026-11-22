package doc

import (
	"bytes"
	"unicode"
	"unicode/utf8"
)

// searchWindow is how much text a search looks at in one go.
const searchWindow = 1 << 20

// matcher finds one needle in windows of text.
type matcher struct {
	needle    []byte
	matchCase bool
	runes     []rune // folded needle, when not matchCase
	firsts    []byte // possible first bytes of a match
	maxLen    int    // longest match in bytes
}

// fold maps the runes that differ only in case to one of them.
func fold(r rune) rune {
	if r < 0x80 {
		if 'A' <= r && r <= 'Z' {
			return r + 'a' - 'A'
		}
		return r
	}
	m := r
	for f := unicode.SimpleFold(r); f != r; f = unicode.SimpleFold(f) {
		m = min(m, f)
	}
	return m
}

func newMatcher(needle string, matchCase bool) *matcher {
	m := &matcher{needle: []byte(needle), matchCase: matchCase, maxLen: len(needle)}
	if matchCase {
		return m
	}
	for _, r := range needle {
		m.runes = append(m.runes, fold(r))
	}
	if len(m.runes) == 0 {
		return m
	}
	m.maxLen = utf8.UTFMax * len(m.runes)
	first, _ := utf8.DecodeRuneInString(needle)
	seen := map[byte]bool{}
	var enc [utf8.UTFMax]byte
	for r, done := first, false; !done; {
		utf8.EncodeRune(enc[:], r)
		if !seen[enc[0]] {
			seen[enc[0]] = true
			m.firsts = append(m.firsts, enc[0])
		}
		r = unicode.SimpleFold(r)
		done = r == first
	}
	if first == utf8.RuneError { // an invalid byte matches itself
		m.firsts = append(m.firsts, needle[0])
	}
	return m
}

// at returns the length of the match that starts at w[i], or -1.
func (m *matcher) at(w []byte, i int) int {
	j := i
	for _, want := range m.runes {
		if j >= len(w) {
			return -1
		}
		r, size := rune(w[j]), 1
		if r >= utf8.RuneSelf {
			r, size = utf8.DecodeRune(w[j:])
		}
		if r != want && fold(r) != want {
			return -1
		}
		j += size
	}
	return j - i
}

// first returns the first match in w that starts at from or later.
func (m *matcher) first(w []byte, from int) (int, int) {
	if m.matchCase {
		if i := bytes.Index(w[from:], m.needle); i >= 0 {
			return from + i, len(m.needle)
		}
		return -1, 0
	}
	// next[k] is the next place of firsts[k] at or after the last position
	// looked at, so that every byte is scanned once per first byte.
	var next [8]int
	firsts := m.firsts[:min(len(m.firsts), len(next))]
	for k := range firsts {
		next[k] = from - 1
	}
	for pos := from; pos < len(w); {
		best := len(w)
		for k, c := range firsts {
			if next[k] < pos {
				if i := bytes.IndexByte(w[pos:], c); i >= 0 {
					next[k] = pos + i
				} else {
					next[k] = len(w)
				}
			}
			best = min(best, next[k])
		}
		if best >= len(w) {
			break
		}
		if n := m.at(w, best); n >= 0 {
			return best, n
		}
		pos = best + 1
	}
	return -1, 0
}

// last returns the last match that lies in w.
func (m *matcher) last(w []byte) (int, int) {
	if m.matchCase {
		if i := bytes.LastIndex(w, m.needle); i >= 0 {
			return i, len(m.needle)
		}
		return -1, 0
	}
	var prev [8]int
	firsts := m.firsts[:min(len(m.firsts), len(prev))]
	for k := range firsts {
		prev[k] = len(w)
	}
	for pos := len(w) - 1; pos >= 0; {
		best := -1
		for k, c := range firsts {
			if prev[k] > pos {
				prev[k] = bytes.LastIndexByte(w[:pos+1], c)
			}
			best = max(best, prev[k])
		}
		if best < 0 {
			break
		}
		if n := m.at(w, best); n >= 0 {
			return best, n
		}
		pos = best - 1
	}
	return -1, 0
}

// Find looks for needle. Going down it returns the first match that starts
// at from or later, going up the last match that ends at from or earlier.
func (d *Doc) Find(needle string, from int64, down, matchCase bool) (Match, bool) {
	if needle == "" {
		return Match{}, false
	}
	m := newMatcher(needle, matchCase)
	var scratch []byte
	total := d.Len()
	overlap := int64(m.maxLen)
	if down {
		for pos := max(from, 0); pos < total; pos += searchWindow {
			w := d.window(pos, min(searchWindow+overlap, total-pos), &scratch)
			if i, n := m.first(w, 0); i >= 0 {
				return Match{pos + int64(i), int64(n)}, true
			}
		}
		return Match{}, false
	}
	for hi := min(from, total); hi > 0; {
		lo := max(0, hi-searchWindow-overlap)
		w := d.window(lo, hi-lo, &scratch)
		if i, n := m.last(w); i >= 0 {
			return Match{lo + int64(i), int64(n)}, true
		}
		if lo == 0 {
			break
		}
		hi = lo + overlap
	}
	return Match{}, false
}

// FindAll returns all matches from the start of the document, not
// overlapping. Stop is polled now and then; when it returns true the search
// ends with nil.
func (d *Doc) FindAll(needle string, matchCase bool, stop func() bool) []Match {
	if needle == "" {
		return nil
	}
	m := newMatcher(needle, matchCase)
	var scratch []byte
	var out []Match
	total := d.Len()
	overlap := int64(m.maxLen)
	for pos := int64(0); pos < total; {
		if stop != nil && stop() {
			return nil
		}
		w := d.window(pos, min(searchWindow+overlap, total-pos), &scratch)
		at := 0
		for {
			i, n := m.first(w, at)
			if i < 0 || (i >= searchWindow && pos+int64(len(w)) < total) {
				break
			}
			out = append(out, Match{pos + int64(i), int64(n)})
			at = i + max(n, 1)
		}
		// Continue after the last match, or at the next window.
		pos = max(pos+int64(min(len(w), searchWindow)), pos+int64(at))
	}
	return out
}
