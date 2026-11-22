package ui

// Text helpers that do not need Windows, so that their tests run anywhere.

import (
	"strconv"
	"strings"

	"github.com/mevdschee/text4win/internal/doc"
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
