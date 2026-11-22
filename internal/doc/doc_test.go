package doc

import (
	"bytes"
	"fmt"
	"math/rand"
	"strings"
	"testing"
)

// model is the obviously correct document the real one is compared with.
type model struct{ text []byte }

func (m *model) lineStarts() []int64 {
	starts := []int64{0}
	for i := 0; i < len(m.text); i++ {
		switch m.text[i] {
		case '\r':
			if i+1 < len(m.text) && m.text[i+1] == '\n' {
				i++
			}
			starts = append(starts, int64(i+1))
		case '\n':
			starts = append(starts, int64(i+1))
		}
	}
	return starts
}

func check(t *testing.T, d *Doc, m *model, what string) {
	t.Helper()
	if got := d.Bytes(0, d.Len()); !bytes.Equal(got, m.text) {
		t.Fatalf("%s: text differs:\n got %q\nwant %q", what, clip(got), clip(m.text))
	}
	starts := m.lineStarts()
	if d.Lines() != int64(len(starts)) {
		t.Fatalf("%s: %d lines, want %d in %q", what, d.Lines(), len(starts), clip(m.text))
	}
	for i := 1; i < len(d.pieces); i++ {
		if d.pieces[i-1].last() == '\r' && d.pieces[i].first() == '\n' {
			t.Fatalf("%s: CR LF split over pieces %d and %d", what, i-1, i)
		}
	}
	for n, s := range starts {
		if n > 300 && n%97 != 0 && n < len(starts)-3 {
			continue
		}
		if got := d.LineStart(int64(n)); got != s {
			t.Fatalf("%s: LineStart(%d) = %d, want %d", what, n, got, s)
		}
		if got := d.LineOf(s); got != int64(n) {
			t.Fatalf("%s: LineOf(%d) = %d, want %d", what, s, got, n)
		}
		end := int64(len(m.text))
		next := end
		if n+1 < len(starts) {
			next = starts[n+1]
			end = next - 1
			if m.text[end] == '\n' && end > s && m.text[end-1] == '\r' {
				end--
			}
		}
		for _, off := range []int64{s, (s + end) / 2, end} {
			a, b, c := d.LineBounds(off)
			if a != s || b != end || c != next {
				t.Fatalf("%s: LineBounds(%d) = %d %d %d, want %d %d %d", what, off, a, b, c, s, end, next)
			}
		}
	}
}

func clip(b []byte) []byte {
	if len(b) > 200 {
		return append(append([]byte{}, b[:200]...), "..."...)
	}
	return b
}

func TestBreaksAcrossPieces(t *testing.T) {
	d := New([]byte("a\rb"), nil, nil)
	m := &model{[]byte("a\rb")}
	d.Replace(2, 0, []byte("\n"), Other) // makes a CR LF over two buffers
	m.text = []byte("a\r\nb")
	check(t, d, m, "join")
	d.Replace(2, 0, []byte("x"), Other) // and splits it again
	m.text = []byte("a\rx\nb")
	check(t, d, m, "split")
	d.Undo()
	m.text = []byte("a\r\nb")
	check(t, d, m, "undo")
	d.Undo()
	m.text = []byte("a\rb")
	check(t, d, m, "undo 2")
	d.Redo()
	d.Redo()
	m.text = []byte("a\rx\nb")
	check(t, d, m, "redo")
}

func randomText(r *rand.Rand, n int) []byte {
	parts := []string{"\r\n", "\n", "\r", "a", "bc", "é", "日本", " ", "\t", "word ", "\r", "\n"}
	var b []byte
	for len(b) < n {
		b = append(b, parts[r.Intn(len(parts))]...)
	}
	return b
}

func TestRandomEdits(t *testing.T) {
	for seed := int64(1); seed <= 30; seed++ {
		r := rand.New(rand.NewSource(seed))
		size := r.Intn(2000)
		if seed%10 == 0 {
			size = 6*blockSize + r.Intn(blockSize) // large enough for the index
		}
		text := randomText(r, size)
		d := New(append([]byte(nil), text...), nil, nil)
		m := &model{text}
		check(t, d, m, "new")
		var history [][]byte
		steps := 200
		if size > blockSize {
			steps = 40
		}
		for step := 0; step < steps; step++ {
			what := fmt.Sprintf("seed %d step %d", seed, step)
			switch op := r.Intn(10); {
			case op < 6:
				off := int64(r.Intn(len(m.text) + 1))
				del := int64(r.Intn(8))
				if r.Intn(20) == 0 {
					del = int64(r.Intn(len(m.text) + 1))
				}
				del = min(del, int64(len(m.text))-off)
				ins := randomText(r, r.Intn(12))
				if del == 0 && len(ins) == 0 {
					continue
				}
				history = append(history, append([]byte(nil), m.text...))
				m.text = append(append(append([]byte{}, m.text[:off]...), ins...), m.text[off+del:]...)
				d.Replace(off, del, ins, Other)
			case op < 8:
				if len(history) == 0 {
					continue
				}
				if _, _, ok := d.Undo(); !ok {
					t.Fatalf("%s: nothing to undo", what)
				}
				history = append(history, append([]byte(nil), m.text...)) // for the redo below
				m.text = history[len(history)-2]
				d.Redo()
				d.Undo()
				history = history[:len(history)-2]
			default:
				needle := string(randomText(r, 1+r.Intn(2)))
				repl := randomText(r, r.Intn(4))
				matches := d.FindAll(needle, true, nil)
				want := strings.Count(string(m.text), needle)
				if len(matches) != want {
					t.Fatalf("%s: FindAll(%q) found %d, want %d", what, needle, len(matches), want)
				}
				if want == 0 {
					continue
				}
				history = append(history, append([]byte(nil), m.text...))
				m.text = bytes.ReplaceAll(m.text, []byte(needle), repl)
				d.ReplaceAll(matches, repl)
			}
			check(t, d, m, what)
		}
		for len(history) > 0 {
			d.Undo()
			m.text = history[len(history)-1]
			history = history[:len(history)-1]
		}
		check(t, d, m, fmt.Sprintf("seed %d all undone", seed))
		if d.Modified() {
			t.Fatalf("seed %d: modified after undoing everything", seed)
		}
	}
}

func TestUndoGroups(t *testing.T) {
	d := New(nil, nil, nil)
	for i, c := range "hello" {
		d.Replace(int64(i), 0, []byte(string(c)), Typing)
	}
	d.BreakUndo()
	for i, c := range " you" {
		d.Replace(int64(5+i), 0, []byte(string(c)), Typing)
	}
	d.Replace(8, 1, nil, Backspace)
	d.Replace(7, 1, nil, Backspace)
	if got := string(d.Bytes(0, d.Len())); got != "hello y" {
		t.Fatalf("got %q", got)
	}
	for _, want := range []string{"hello you", "hello", ""} {
		d.Undo()
		if got := string(d.Bytes(0, d.Len())); got != want {
			t.Fatalf("after undo: %q, want %q", got, want)
		}
	}
	if d.CanUndo() || d.Modified() {
		t.Fatal("still something to undo")
	}
	d.Redo()
	d.SetSaved()
	d.Redo()
	d.Undo()
	if d.Modified() {
		t.Fatal("modified at the saved state")
	}
}

func TestFind(t *testing.T) {
	text := "Hello hello HELLO straße STRASSE Ünïcode ünÏcode K k"
	d := New([]byte(text), nil, nil)
	d.Replace(3, 0, []byte("x"), Other) // more than one piece
	d.Undo()
	d.Replace(8, 1, []byte("l"), Other)
	find := func(needle string, from int64, down, matchCase bool) int64 {
		m, ok := d.Find(needle, from, down, matchCase)
		if !ok {
			return -1
		}
		if !strings.EqualFold(text[m.Off:m.Off+m.N], needle) {
			t.Fatalf("Find(%q) returned %q", needle, text[m.Off:m.Off+m.N])
		}
		return m.Off
	}
	for _, c := range []struct {
		needle   string
		from     int64
		down, cs bool
		want     int64
	}{
		{"hello", 0, true, true, 6},
		{"hello", 0, true, false, 0},
		{"hello", 1, true, false, 6},
		{"hello", 7, true, false, 12},
		{"hello", 13, true, false, -1},
		{"HELLO", int64(len(text)), false, false, 12},
		{"hello", 16, false, false, 6},
		{"hello", 11, false, false, 6},
		{"hello", 10, false, false, 0},
		{"hello", 4, false, false, -1},
		{"ünïcode", 0, true, false, int64(strings.Index(text, "Ünïcode"))},
		{"ünïcode", 0, true, true, -1},
		{"ÜNÏCODE", int64(len(text)), false, false, int64(strings.Index(text, "ünÏcode"))},
		{"k", 0, true, false, int64(strings.Index(text, "K"))},
		{"", 0, true, false, -1},
	} {
		if got := find(c.needle, c.from, c.down, c.cs); got != c.want {
			t.Errorf("Find(%q, %d, down=%v, case=%v) = %d, want %d", c.needle, c.from, c.down, c.cs, got, c.want)
		}
	}
}

func TestFindAcrossWindows(t *testing.T) {
	big := bytes.Repeat([]byte("0123456789abcdef"), 3*searchWindow/16)
	at := []int{5, searchWindow - 3, 2*searchWindow + 100, len(big) - 6}
	for _, i := range at {
		copy(big[i:], "NeEdLe")
	}
	d := New(big, nil, nil)
	d.Replace(int64(searchWindow+48), 1, []byte("0"), Other) // split in pieces
	for _, cs := range []bool{true, false} {
		needle := "NeEdLe"
		if !cs {
			needle = "needle"
		}
		all := d.FindAll(needle, cs, nil)
		if len(all) != len(at) {
			t.Fatalf("FindAll found %d, want %d", len(all), len(at))
		}
		from := int64(0)
		for _, want := range at {
			m, ok := d.Find(needle, from, true, cs)
			if !ok || m.Off != int64(want) {
				t.Fatalf("down from %d: %v %v, want %d", from, m, ok, want)
			}
			from = m.Off + m.N
		}
		from = d.Len()
		for k := len(at) - 1; k >= 0; k-- {
			m, ok := d.Find(needle, from, false, cs)
			if !ok || m.Off != int64(at[k]) {
				t.Fatalf("up from %d: %v %v, want %d", from, m, ok, at[k])
			}
			from = m.Off
		}
		if _, ok := d.Find(needle, from, false, cs); ok {
			t.Fatal("found one more going up")
		}
	}
}

func TestEncodings(t *testing.T) {
	text := "Grüße, 日本語 😀\r\nline 2 €\n"
	d := New([]byte(text), nil, nil)
	for _, enc := range Encodings {
		var buf bytes.Buffer
		if err := d.WriteTo(&buf, enc); err != nil {
			t.Fatal(err)
		}
		got, det := Load(buf.Bytes(), true, nil, nil)
		want := text
		if enc == ANSI {
			want = "Grüße, ??? ?\r\nline 2 €\n"
			if d.FitsANSI() {
				t.Error("FitsANSI is true for text with Japanese")
			}
		}
		if det != enc && !(enc == ANSI && det == ANSI) {
			t.Errorf("%v: detected as %v", enc, det)
		}
		if s := string(got.Bytes(0, got.Len())); s != want {
			t.Errorf("%v: got %q, want %q", enc, s, want)
		}
	}
	if !New([]byte("café €"), nil, nil).FitsANSI() {
		t.Error("FitsANSI is false for Windows-1252 text")
	}
	for _, c := range []struct {
		data string
		want Encoding
	}{
		{"plain", UTF8},
		{"", UTF8},
		{"caf\xe9", ANSI},
		{"h\x00i\x00\r\x00\n\x00", UTF16LE},
		{"\x00h\x00i\x00\r\x00\n", UTF16BE},
	} {
		if got, _ := Detect([]byte(c.data), true); got != c.want {
			t.Errorf("Detect(%q) = %v, want %v", c.data, got, c.want)
		}
	}
	if got, _ := Detect([]byte("abc\xe6\x97"), false); got != UTF8 {
		t.Errorf("a sample that ends inside a character is detected as %v", got)
	}
	for text, want := range map[string]EOL{"a\r\nb\n": CRLF, "a\nb\r\n": LF, "a\rb": CR, "ab": CRLF} {
		if got := New([]byte(text), nil, nil).EOL(); got != want {
			t.Errorf("EOL(%q) = %v, want %v", text, got, want)
		}
	}
}

func TestLongLines(t *testing.T) {
	// One line that is much longer than the index blocks, between others.
	long := bytes.Repeat([]byte("x"), 10*blockSize)
	text := append(append([]byte("first\r\n"), long...), "\nlast"...)
	d := New(text, nil, nil)
	m := &model{text}
	check(t, d, m, "long")
	s, e, n := d.LineBounds(7 + 5*blockSize)
	if s != 7 || e != int64(7+len(long)) || n != e+1 {
		t.Fatalf("LineBounds in the long line = %d %d %d", s, e, n)
	}
	d.Replace(7+3*blockSize, 0, []byte("\r\n"), Other)
	m.text = append(append(append([]byte{}, text[:7+3*blockSize]...), "\r\n"...), text[7+3*blockSize:]...)
	check(t, d, m, "long, split")
}

func BenchmarkIndex(b *testing.B) {
	text := bytes.Repeat([]byte("a line of text that is about sixty characters long, or so\r\n"), 1<<20)
	b.SetBytes(int64(len(text)))
	for b.Loop() {
		New(text, nil, nil)
	}
}
