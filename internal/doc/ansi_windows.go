package doc

import (
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"golang.org/x/sys/windows"
)

// On Windows ANSI is the system code page, like in Notepad.

var (
	kernel32                = windows.NewLazySystemDLL("kernel32.dll")
	procWideCharToMultiByte = kernel32.NewProc("WideCharToMultiByte")
	procIsDBCSLeadByte      = kernel32.NewProc("IsDBCSLeadByte")
)

func isLeadByte(c byte) bool {
	if c < 0x80 {
		return false
	}
	r, _, _ := procIsDBCSLeadByte.Call(uintptr(c))
	return r != 0
}

const (
	cpACP             = 0
	wcNoBestFitChars  = 0x0400
	ansiChunk         = 16 << 20
	maxAnsiCharLength = 2 // a lead and a trail byte, in the double byte code pages
)

func ansiDecode(b []byte) []byte {
	out := make([]byte, 0, len(b)+len(b)/8)
	var wide []uint16
	for len(b) > 0 {
		n := min(len(b), ansiChunk)
		if n < len(b) && n > 0 && isLeadByte(b[n-1]) && !trailAt(b, n-1) {
			n-- // do not split a double byte character
		}
		if cap(wide) < n {
			wide = make([]uint16, n)
		}
		m, err := windows.MultiByteToWideChar(cpACP, 0, &b[0], int32(n), &wide[0], int32(n))
		if err != nil || m <= 0 {
			for _, c := range b[:n] { // cannot happen; keep the bytes as Latin-1
				out = utf8.AppendRune(out, rune(c))
			}
		} else {
			out = appendUTF16(out, wide[:m])
		}
		b = b[n:]
	}
	return out
}

// ansiSplit returns how many bytes at the start of b are whole characters
// when more bytes follow.
func ansiSplit(b []byte) int {
	if n := len(b); n > 1 && isLeadByte(b[n-1]) && !trailAt(b, n-1) {
		return n - 1
	}
	return len(b)
}

// trailAt reports whether b[i] is the second byte of a double byte
// character, by walking from the start of the run of lead bytes before it.
func trailAt(b []byte, i int) bool {
	j := i
	for j > 0 && isLeadByte(b[j-1]) {
		j--
	}
	return (i-j)%2 == 1
}

func appendUTF16(out []byte, w []uint16) []byte {
	for i := 0; i < len(w); i++ {
		r := rune(w[i])
		if utf16.IsSurrogate(r) {
			r2 := rune(utf8.RuneError)
			if i+1 < len(w) {
				r2 = rune(w[i+1])
			}
			if dec := utf16.DecodeRune(r, r2); dec != utf8.RuneError {
				r = dec
				i++
			} else {
				r = utf8.RuneError
			}
		}
		out = utf8.AppendRune(out, r)
	}
	return out
}

func ansiEncode(b []byte) (out []byte, lossy bool) {
	if len(b) == 0 {
		return nil, false
	}
	wide := utf16.Encode([]rune(string(b)))
	if len(wide) == 0 {
		return nil, false
	}
	out = make([]byte, len(wide)*maxAnsiCharLength)
	var usedDefault int32
	n, _, _ := procWideCharToMultiByte.Call(cpACP, wcNoBestFitChars,
		uintptr(unsafe.Pointer(&wide[0])), uintptr(len(wide)),
		uintptr(unsafe.Pointer(&out[0])), uintptr(len(out)), 0, uintptr(unsafe.Pointer(&usedDefault)))
	return out[:n], usedDefault != 0
}
