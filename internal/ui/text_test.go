package ui

import (
	"strings"
	"testing"
	"unicode/utf16"

	"github.com/mevdschee/text4win/internal/doc"
)

func TestHeaderParts(t *testing.T) {
	for _, c := range []struct {
		format string
		want   [3]string
	}{
		{"&f", [3]string{"", "notes.txt", ""}},
		{"Page &p", [3]string{"", "Page 7", ""}},
		{"&l&f&rPage &P", [3]string{"notes.txt", "", "Page 7"}},
		{"&d &t&c&&x&", [3]string{"", "1/2/2026 3:04 PM&x&", ""}},
		{"start&l left &c mid &r right", [3]string{" left ", "start mid ", " right"}},
		{"", [3]string{}},
		{"&q", [3]string{"", "&q", ""}},
	} {
		got := headerParts(c.format, "notes.txt", 7, "1/2/2026", "3:04 PM")
		if got != c.want {
			t.Errorf("headerParts(%q) = %q, want %q", c.format, got, c.want)
		}
	}
}

func TestConvertEOL(t *testing.T) {
	in := "a\r\nb\nc\rd\r\n\r\ne\n"
	for eol, want := range map[doc.EOL]string{
		doc.CRLF: "a\r\nb\r\nc\r\nd\r\n\r\ne\r\n",
		doc.LF:   "a\nb\nc\nd\n\ne\n",
		doc.CR:   "a\rb\rc\rd\r\re\r",
	} {
		if got := string(convertEOL([]byte(in), eol)); got != want {
			t.Errorf("convertEOL to %v = %q, want %q", eol, got, want)
		}
	}
}

func TestExpandTabs(t *testing.T) {
	u := func(s string) []uint16 {
		var out []uint16
		for _, r := range s {
			out = append(out, uint16(r))
		}
		return out
	}
	for in, want := range map[string]string{
		"no tabs":     "no tabs",
		"a\tb":        "a       b",
		"\tx":         "        x",
		"1234567\t":   "1234567 ",
		"12345678\tz": "12345678        z",
	} {
		if got := string(runes(expandTabs(u(in)))); got != want {
			t.Errorf("expandTabs(%q) = %q, want %q", in, got, want)
		}
	}
}

func runes(u []uint16) []rune {
	out := make([]rune, len(u))
	for i, c := range u {
		out[i] = rune(c)
	}
	return out
}

func TestHexRow(t *testing.T) {
	got := string(utf16.Decode(hexRow(nil, 8, 0x1F0, []byte("Hi\x00\xff"))))
	want := "000001F0  48 69 00 FF" + strings.Repeat(" ", 36) + "  Hi.." + strings.Repeat(" ", 12)
	if got != want {
		t.Errorf("hexRow = %q, want %q", got, want)
	}
	if n := hexAddrDigits(1 << 32); n != 10 {
		t.Errorf("hexAddrDigits(4 GB) = %d, want 10", n)
	}
	if n := hexAddrDigits(1<<32 - 1); n != 8 {
		t.Errorf("hexAddrDigits(4 GB - 1) = %d, want 8", n)
	}
}

func TestParseHex(t *testing.T) {
	for in, want := range map[string]string{
		"4D 5A":      "MZ",
		"4d5a\r\n00": "MZ\x00",
		" 41 ":       "A",
	} {
		if got, ok := parseHex([]byte(in)); !ok || string(got) != want {
			t.Errorf("parseHex(%q) = %q, %v", in, got, ok)
		}
	}
	for _, in := range []string{"", "  ", "4", "4D 5", "4 D", "xy", "0x41"} {
		if got, ok := parseHex([]byte(in)); ok {
			t.Errorf("parseHex(%q) = %q, want no bytes", in, got)
		}
	}
	if got := formatHex([]byte("MZ\x00")); got != "4D 5A 00" {
		t.Errorf("formatHex = %q", got)
	}
}
