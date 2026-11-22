package doc

import (
	"bufio"
	"encoding/binary"
	"io"
	"sync/atomic"
	"unicode/utf16"
	"unicode/utf8"
)

// Encoding is how a document is stored in its file.
type Encoding int

// The encodings, in the order of the Save As dialog.
const (
	ANSI Encoding = iota
	UTF16LE
	UTF16BE
	UTF8
	UTF8BOM
)

// Encodings lists all encodings in the order of the Save As dialog.
var Encodings = []Encoding{ANSI, UTF16LE, UTF16BE, UTF8, UTF8BOM}

func (e Encoding) String() string {
	switch e {
	case ANSI:
		return "ANSI"
	case UTF16LE:
		return "UTF-16 LE"
	case UTF16BE:
		return "UTF-16 BE"
	case UTF8BOM:
		return "UTF-8 with BOM"
	}
	return "UTF-8"
}

// EOL is a kind of line break.
type EOL int

const (
	CRLF EOL = iota
	LF
	CR
)

func (e EOL) String() string {
	switch e {
	case LF:
		return "Unix (LF)"
	case CR:
		return "Macintosh (CR)"
	}
	return "Windows (CRLF)"
}

// Bytes returns the line break itself.
func (e EOL) Bytes() []byte {
	switch e {
	case LF:
		return []byte("\n")
	case CR:
		return []byte("\r")
	}
	return []byte("\r\n")
}

// EOL returns the kind of the first line break of the document, CRLF when
// it has none.
func (d *Doc) EOL() EOL {
	if d.Lines() < 2 {
		return CRLF
	}
	_, end, next := d.LineBounds(0)
	switch {
	case next-end == 2:
		return CRLF
	case d.ByteAt(end) == '\r':
		return CR
	}
	return LF
}

// Detect guesses the encoding of a file from its first bytes and returns
// the size of its byte order mark. Complete tells that sample is the whole
// file. Like Notepad it looks for a byte order mark, then for UTF-16
// without one, then for valid UTF-8, and falls back to ANSI.
func Detect(sample []byte, complete bool) (Encoding, int) {
	switch {
	case len(sample) >= 3 && sample[0] == 0xEF && sample[1] == 0xBB && sample[2] == 0xBF:
		return UTF8BOM, 3
	case len(sample) >= 2 && sample[0] == 0xFF && sample[1] == 0xFE:
		return UTF16LE, 2
	case len(sample) >= 2 && sample[0] == 0xFE && sample[1] == 0xFF:
		return UTF16BE, 2
	}
	// UTF-16 without a mark: text in western scripts has a zero in every
	// other byte.
	if n := len(sample) &^ 1; n >= 2 {
		var even, odd int
		for i := 0; i < n; i += 2 {
			if sample[i] == 0 {
				even++
			}
			if sample[i+1] == 0 {
				odd++
			}
		}
		half := n / 2
		switch {
		case odd*10 >= half*4 && even*20 <= half:
			return UTF16LE, 0
		case even*10 >= half*4 && odd*20 <= half:
			return UTF16BE, 0
		}
	}
	if !complete { // the sample may end inside a character
		for cut := 1; cut <= utf8.UTFMax && cut <= len(sample); cut++ {
			if utf8.RuneStart(sample[len(sample)-cut]) {
				if !utf8.FullRune(sample[len(sample)-cut:]) {
					sample = sample[:len(sample)-cut]
				}
				break
			}
		}
	}
	if utf8.Valid(sample) {
		return UTF8, 0
	}
	return ANSI, 0
}

// sampleSize is how much of a file that is too large to check completely
// is used to detect its encoding.
const sampleSize = 1 << 20

// Load makes a document of the contents of a file. UTF-8 is used as it is,
// without a copy, so raw must stay unchanged (and mapped) as long as the
// document lives; other encodings are converted in memory. Complete tells
// whether raw may be checked as a whole to detect the encoding, which is
// too slow for huge files. A nil document means that stop was set.
func Load(raw []byte, complete bool, progress func(done int64), stop *atomic.Bool) (*Doc, Encoding) {
	sample := raw
	if !complete && len(sample) > sampleSize {
		sample = sample[:sampleSize]
	}
	enc, bom := Detect(sample, len(sample) == len(raw))
	text := raw[bom:]
	switch enc {
	case UTF16LE, UTF16BE:
		text = decodeUTF16(text, enc == UTF16BE)
	case ANSI:
		text = ansiDecode(text)
	}
	d := New(text, progress, stop)
	if stop != nil && stop.Load() {
		return nil, enc
	}
	return d, enc
}

func decodeUTF16(b []byte, bigEndian bool) []byte {
	var order binary.ByteOrder = binary.LittleEndian
	if bigEndian {
		order = binary.BigEndian
	}
	out := make([]byte, 0, len(b)/2+len(b)/8)
	for i := 0; i+1 < len(b); i += 2 {
		r := rune(order.Uint16(b[i:]))
		if utf16.IsSurrogate(r) {
			r2 := rune(utf8.RuneError)
			if i+3 < len(b) {
				r2 = rune(order.Uint16(b[i+2:]))
			}
			if dec := utf16.DecodeRune(r, r2); dec != utf8.RuneError {
				r = dec
				i += 2
			} else {
				r = utf8.RuneError
			}
		}
		out = utf8.AppendRune(out, r)
	}
	return out
}

// splitRunes returns how many bytes at the start of b are whole characters;
// the rest (at most 3 bytes) is the start of a character that continues in
// the next chunk.
func splitRunes(b []byte) int {
	for cut := 1; cut <= utf8.UTFMax && cut <= len(b); cut++ {
		if c := b[len(b)-cut]; utf8.RuneStart(c) {
			if c >= utf8.RuneSelf && !utf8.FullRune(b[len(b)-cut:]) {
				return len(b) - cut
			}
			break
		}
	}
	return len(b)
}

// chunks calls fn with the text in parts that never split a character.
func (d *Doc) chunks(fn func([]byte) error) error {
	const size = 1 << 20
	var carry []byte
	for off := int64(0); off < d.Len(); {
		c := d.Chunk(off)
		if len(c) > size {
			c = c[:size]
		}
		off += int64(len(c))
		if len(carry) > 0 {
			c = append(carry, c...)
			carry = nil
		}
		if off < d.Len() {
			n := splitRunes(c)
			carry = append([]byte(nil), c[n:]...)
			c = c[:n]
		}
		if err := fn(c); err != nil {
			return err
		}
	}
	return nil
}

// WriteTo writes the document in an encoding.
func (d *Doc) WriteTo(w io.Writer, enc Encoding) error {
	bw := bufio.NewWriterSize(w, 1<<20)
	var err error
	switch enc {
	case UTF8, UTF8BOM:
		if enc == UTF8BOM {
			bw.Write([]byte{0xEF, 0xBB, 0xBF})
		}
		for off := int64(0); off < d.Len() && err == nil; {
			c := d.Chunk(off)
			_, err = bw.Write(c)
			off += int64(len(c))
		}
	case UTF16LE, UTF16BE:
		var order binary.AppendByteOrder = binary.LittleEndian
		if enc == UTF16BE {
			order = binary.BigEndian
		}
		bw.Write(order.AppendUint16(nil, 0xFEFF))
		var out []byte
		err = d.chunks(func(c []byte) error {
			out = out[:0]
			for len(c) > 0 {
				r, size := utf8.DecodeRune(c)
				c = c[size:]
				if r1, r2 := utf16.EncodeRune(r); r1 != utf8.RuneError {
					out = order.AppendUint16(order.AppendUint16(out, uint16(r1)), uint16(r2))
				} else {
					out = order.AppendUint16(out, uint16(r))
				}
			}
			_, err := bw.Write(out)
			return err
		})
	case ANSI:
		err = d.chunks(func(c []byte) error {
			out, _ := ansiEncode(c)
			_, err := bw.Write(out)
			return err
		})
	}
	if err != nil {
		return err
	}
	return bw.Flush()
}

// FitsANSI reports whether the document can be saved as ANSI without losing
// characters.
func (d *Doc) FitsANSI() bool {
	fits := true
	d.chunks(func(c []byte) error {
		ascii := true
		for _, b := range c {
			if b >= utf8.RuneSelf {
				ascii = false
				break
			}
		}
		if !ascii {
			if _, lossy := ansiEncode(c); lossy {
				fits = false
				return io.EOF
			}
		}
		return nil
	})
	return fits
}
