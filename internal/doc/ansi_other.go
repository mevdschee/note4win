//go:build !windows

package doc

import "unicode/utf8"

// Outside Windows (the tests) ANSI is Windows-1252.
var cp1252 = [32]rune{
	0x20AC, 0x81, 0x201A, 0x0192, 0x201E, 0x2026, 0x2020, 0x2021, 0x02C6, 0x2030, 0x0160, 0x2039, 0x0152, 0x8D, 0x017D, 0x8F,
	0x90, 0x2018, 0x2019, 0x201C, 0x201D, 0x2022, 0x2013, 0x2014, 0x02DC, 0x2122, 0x0161, 0x203A, 0x0153, 0x9D, 0x017E, 0x0178,
}

func ansiDecode(b []byte) []byte {
	out := make([]byte, 0, len(b)+len(b)/8)
	for _, c := range b {
		r := rune(c)
		if c >= 0x80 && c < 0xA0 {
			r = cp1252[c-0x80]
		}
		out = utf8.AppendRune(out, r)
	}
	return out
}

func ansiEncode(b []byte) (out []byte, lossy bool) {
	out = make([]byte, 0, len(b))
next:
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		b = b[size:]
		if r < 0x80 || (r >= 0xA0 && r <= 0xFF) {
			out = append(out, byte(r))
			continue
		}
		for i, c := range cp1252 {
			if c == r {
				out = append(out, byte(0x80+i))
				continue next
			}
		}
		out = append(out, '?')
		lossy = true
	}
	return out, lossy
}
