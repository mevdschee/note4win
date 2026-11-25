package ui

// Text helpers that do not need Windows, so that their tests run anywhere.

import (
	"strconv"
	"strings"

	"github.com/mevdschee/note4win/internal/doc"
)

// headerParts splits a header or footer in its left, centre and right text.
func headerParts(format, file string, page int, date, time string) (parts [3]string) {
	at := 1 // centred, unless a code says otherwise
	var b strings.Builder
	flush := func() {
		parts[at] += b.String()
		b.Reset()
	}
	for i := 0; i < len(format); i++ {
		if format[i] != '&' || i+1 >= len(format) {
			b.WriteByte(format[i])
			continue
		}
		i++
		switch format[i] | 0x20 {
		case 'f':
			b.WriteString(file)
		case 'p':
			b.WriteString(strconv.Itoa(page))
		case 'd':
			b.WriteString(date)
		case 't':
			b.WriteString(time)
		case 'l':
			flush()
			at = 0
		case 'c':
			flush()
			at = 1
		case 'r':
			flush()
			at = 2
		default:
			if format[i] == '&' {
				b.WriteByte('&')
			} else {
				b.WriteByte('&')
				b.WriteByte(format[i])
			}
		}
	}
	flush()
	return parts
}

// expandTabs replaces tabs by spaces up to the next multiple of 8 columns.
func expandTabs(text []uint16) []uint16 {
	hasTab := false
	for _, c := range text {
		if c == '\t' {
			hasTab = true
			break
		}
	}
	if !hasTab {
		return text
	}
	out := make([]uint16, 0, len(text)+16)
	for _, c := range text {
		if c != '\t' {
			out = append(out, c)
			continue
		}
		out = append(out, ' ')
		for len(out)%8 != 0 {
			out = append(out, ' ')
		}
	}
	return out
}

// convertEOL rewrites all line breaks of text to eol.
func convertEOL(text []byte, eol doc.EOL) []byte {
	br := eol.Bytes()
	out := make([]byte, 0, len(text)+len(text)/16)
	for i := 0; i < len(text); i++ {
		switch c := text[i]; c {
		case '\r':
			if i+1 < len(text) && text[i+1] == '\n' {
				i++
			}
			out = append(out, br...)
		case '\n':
			out = append(out, br...)
		default:
			out = append(out, c)
		}
	}
	return out
}

// ---- binary mode ----

// hexCols is the number of bytes in a row of the binary mode.
const hexCols = 16

const hexDigits = "0123456789ABCDEF"

// hexAddrDigits is the width of the address column for a file of size bytes.
func hexAddrDigits(size int64) int {
	n := 8
	for n < 16 && size>>(4*n) != 0 {
		n += 2
	}
	return n
}

// The cells of a row: the address, two blanks, the bytes in hex with a
// blank between them, two blanks and the bytes as characters.
func hexCol(digits, k int) int { return digits + 2 + 3*k }
func chrCol(digits, k int) int { return digits + 2 + 3*hexCols + 1 + k }
func hexCells(digits int) int  { return chrCol(digits, hexCols) }

// hexRow appends the row for the bytes b at off to out, one unit per cell.
func hexRow(out []uint16, digits int, off int64, b []byte) []uint16 {
	start := len(out)
	for i := 0; i < hexCells(digits); i++ {
		out = append(out, ' ')
	}
	row := out[start:]
	for i := 0; i < digits; i++ {
		row[digits-1-i] = uint16(hexDigits[(off>>(4*i))&15])
	}
	for k, c := range b {
		h := hexCol(digits, k)
		row[h], row[h+1] = uint16(hexDigits[c>>4]), uint16(hexDigits[c&15])
		if c < 0x20 || c > 0x7E {
			c = '.'
		}
		row[chrCol(digits, k)] = uint16(c)
	}
	return out
}

// hexVal returns the value of a hex digit.
func hexVal(r rune) (byte, bool) {
	switch {
	case '0' <= r && r <= '9':
		return byte(r - '0'), true
	case 'a' <= r|0x20 && r|0x20 <= 'f':
		return byte(r|0x20-'a') + 10, true
	}
	return 0, false
}

// parseHex reads bytes written as pairs of hex digits, with or without
// blanks and line breaks around the pairs: "4D 5A" or "4d5a".
func parseHex(text []byte) ([]byte, bool) {
	out := make([]byte, 0, len(text)/2)
	for i := 0; i < len(text); i++ {
		switch text[i] {
		case ' ', '\t', '\r', '\n':
			continue
		}
		if i+1 >= len(text) {
			return nil, false
		}
		hi, ok1 := hexVal(rune(text[i]))
		lo, ok2 := hexVal(rune(text[i+1]))
		if !ok1 || !ok2 {
			return nil, false
		}
		out = append(out, hi<<4|lo)
		i++
	}
	return out, len(out) > 0
}

// formatHex writes bytes as pairs of hex digits with blanks between them.
func formatHex(b []byte) string {
	var sb strings.Builder
	for i, c := range b {
		if i > 0 {
			sb.WriteByte(' ')
		}
		sb.WriteByte(hexDigits[c>>4])
		sb.WriteByte(hexDigits[c&15])
	}
	return sb.String()
}
