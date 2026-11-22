package doc

import (
	"sort"
	"sync/atomic"
)

// bigInsert is the size from which inserted text gets its own indexed
// buffer instead of going into the add buffer.
const bigInsert = 1 << 20

// piece is a part of a buffer that is part of the document.
type piece struct {
	buf *buffer
	off int64
	n   int64
	brk int64 // line breaks in the piece
}

func (p piece) first() byte { return p.buf.data[p.off] }
func (p piece) last() byte  { return p.buf.data[p.off+p.n-1] }

// Kind tells which edits may be undone together.
type Kind int

const (
	Other     Kind = iota // never joined with the previous edit
	Typing                // inserting at the end of the previous insert
	Backspace             // deleting right before the previous delete
	Delete                // deleting at the place of the previous delete
)

// rec is one step of the undo or redo list: at off, in bytes of the document
// are swapped with the out pieces. Doing that twice restores the document,
// so undo and redo are the same operation.
type rec struct {
	off    int64
	out    []piece
	in     int64
	kind   Kind
	before int64 // document state before the step
	after  int64
}

// Doc is a text document. It is not safe for concurrent use.
type Doc struct {
	pieces []piece
	cumLen []int64 // cumLen[i]: bytes before pieces[i]; one extra entry
	cumBrk []int64 // cumBrk[i]: line breaks before pieces[i]

	add *buffer // small inserts, append only

	undo, redo []*rec
	state      int64 // identifies the current text
	saved      int64 // state that is on disk
	counter    int64
	group      bool // the next edit may join the last undo step
}

var crlfBuf = &buffer{data: []byte("\r\n")}

// New makes a document of UTF-8 text without copying it; the text must not
// change afterwards. Progress and stop are as in the loading functions.
func New(text []byte, progress func(done int64), stop *atomic.Bool) *Doc {
	d := &Doc{add: &buffer{}}
	if len(text) > 0 {
		b := newBuffer(text, progress, stop)
		d.pieces = []piece{mkPiece(b, 0, int64(len(text)))}
	}
	d.reindex()
	return d
}

func mkPiece(b *buffer, off, n int64) piece {
	return piece{buf: b, off: off, n: n, brk: b.breaks(off, off+n)}
}

func (d *Doc) reindex() {
	n := len(d.pieces) + 1
	if cap(d.cumLen) < n {
		d.cumLen = make([]int64, n, n+n/4+8)
		d.cumBrk = make([]int64, n, n+n/4+8)
	}
	d.cumLen, d.cumBrk = d.cumLen[:n], d.cumBrk[:n]
	var l, b int64
	for i, p := range d.pieces {
		d.cumLen[i], d.cumBrk[i] = l, b
		l += p.n
		b += p.brk
	}
	d.cumLen[n-1], d.cumBrk[n-1] = l, b
}

// Len is the size of the text in bytes.
func (d *Doc) Len() int64 { return d.cumLen[len(d.pieces)] }

// Lines is the number of lines; an empty document has one.
func (d *Doc) Lines() int64 { return d.cumBrk[len(d.pieces)] + 1 }

// Modified reports whether the text differs from the saved text.
func (d *Doc) Modified() bool { return d.state != d.saved }

// SetSaved marks the current text as the saved text.
func (d *Doc) SetSaved() { d.saved = d.state; d.group = false }

// CanUndo and CanRedo report whether there is something to undo or redo.
func (d *Doc) CanUndo() bool { return len(d.undo) > 0 }
func (d *Doc) CanRedo() bool { return len(d.redo) > 0 }

// BreakUndo makes the next edit a separate undo step.
func (d *Doc) BreakUndo() { d.group = false }

// find returns the piece that holds offset off and the offset in it. At the
// end of the document that is (len(pieces), 0).
func (d *Doc) find(off int64) (int, int64) {
	i := sort.Search(len(d.pieces), func(i int) bool { return d.cumLen[i+1] > off })
	return i, off - d.cumLen[i]
}

// extract returns pieces for the n bytes at off.
func (d *Doc) extract(off, n int64) []piece {
	if n <= 0 {
		return nil
	}
	i, a := d.find(off)
	var out []piece
	for ; n > 0 && i < len(d.pieces); i++ {
		p := d.pieces[i]
		take := min(p.n-a, n)
		if a == 0 && take == p.n {
			out = append(out, p)
		} else {
			out = append(out, mkPiece(p.buf, p.off+a, take))
		}
		n -= take
		a = 0
	}
	return out
}

// fix joins pieces that are neighbours in their buffer, drops empty ones
// and moves a CR LF that is split over two pieces to a piece of its own.
func fix(ps []piece) []piece {
	out := make([]piece, 0, len(ps)+2)
	for _, p := range ps {
		if p.n == 0 {
			continue
		}
		if len(out) > 0 {
			q := &out[len(out)-1]
			split := q.last() == '\r' && p.first() == '\n'
			if q.buf == p.buf && q.off+q.n == p.off && q.buf != crlfBuf {
				q.n += p.n
				q.brk += p.brk
				if split {
					q.brk--
				}
				continue
			}
			if split {
				q.n--
				q.brk--
				if q.n == 0 {
					out = out[:len(out)-1]
				}
				out = append(out, piece{buf: crlfBuf, n: 2, brk: 1})
				p.off++
				p.n--
				p.brk--
				if p.n == 0 {
					continue
				}
			}
		}
		out = append(out, p)
	}
	return out
}

// splice replaces del bytes at off with the pieces ins.
func (d *Doc) splice(off, del int64, ins []piece) {
	i, a := d.find(off)
	j, b := d.find(off + del)
	lo, hi := i, j // pieces[lo:hi] are replaced
	var mid []piece
	if a > 0 {
		p := d.pieces[i]
		mid = append(mid, mkPiece(p.buf, p.off, a))
	} else if i > 0 {
		lo = i - 1
		mid = append(mid, d.pieces[lo])
	}
	mid = append(mid, ins...)
	if j < len(d.pieces) {
		p := d.pieces[j]
		hi = j + 1
		if b > 0 {
			mid = append(mid, mkPiece(p.buf, p.off+b, p.n-b))
		} else {
			mid = append(mid, p)
		}
	}
	mid = fix(mid)
	out := make([]piece, 0, len(d.pieces)-(hi-lo)+len(mid)+16)
	out = append(out, d.pieces[:lo]...)
	out = append(out, mid...)
	out = append(out, d.pieces[hi:]...)
	d.pieces = out
	d.reindex()
}

// store keeps new text and returns the piece for it.
func (d *Doc) store(text []byte) piece {
	if len(text) >= bigInsert {
		return mkPiece(newBuffer(append([]byte(nil), text...), nil, nil), 0, int64(len(text)))
	}
	off := int64(len(d.add.data))
	d.add.data = append(d.add.data, text...)
	return mkPiece(d.add, off, int64(len(text)))
}

func piecesLen(ps []piece) (n int64) {
	for _, p := range ps {
		n += p.n
	}
	return n
}

// Replace replaces del bytes at off with text, as one undo step or, when
// kind allows it, as part of the previous step.
func (d *Doc) Replace(off, del int64, text []byte, kind Kind) {
	off = max(0, min(off, d.Len()))
	del = max(0, min(del, d.Len()-off))
	if del == 0 && len(text) == 0 {
		return
	}
	removed := d.extract(off, del)
	var ins []piece
	if len(text) > 0 {
		ins = []piece{d.store(text)}
	}
	d.splice(off, del, ins)
	d.redo = nil
	d.counter++
	before := d.state
	d.state = d.counter
	if d.group && len(d.undo) > 0 && kind != Other {
		r := d.undo[len(d.undo)-1]
		joined := r.kind == kind
		switch {
		case !joined:
		case kind == Typing && del == 0 && off == r.off+r.in:
			r.in += int64(len(text))
		case kind == Backspace && len(text) == 0 && r.in == 0 && off+del == r.off:
			r.out = append(removed, r.out...)
			r.off = off
		case kind == Delete && len(text) == 0 && r.in == 0 && off == r.off:
			r.out = append(r.out, removed...)
		default:
			joined = false
		}
		if joined {
			r.after = d.state
			return
		}
	}
	d.undo = append(d.undo, &rec{off: off, out: removed, in: int64(len(text)), kind: kind, before: before, after: d.state})
	d.group = true
}

// Match is a found text: n bytes at off.
type Match struct{ Off, N int64 }

// ReplaceAll replaces all matches, which must be sorted and not overlap,
// with text, as one undo step.
func (d *Doc) ReplaceAll(matches []Match, text []byte) {
	if len(matches) == 0 {
		return
	}
	old := d.pieces
	var rp piece
	if len(text) > 0 {
		rp = d.store(text)
	}
	np := make([]piece, 0, len(old)+2*len(matches))
	var prev int64
	for _, m := range matches {
		np = append(np, d.extract(prev, m.Off-prev)...)
		if rp.n > 0 {
			np = append(np, rp)
		}
		prev = m.Off + m.N
	}
	np = append(np, d.extract(prev, d.Len()-prev)...)
	d.pieces = fix(np)
	d.reindex()
	d.redo = nil
	d.counter++
	d.undo = append(d.undo, &rec{out: old, in: d.Len(), before: d.state, after: d.counter})
	d.state = d.counter
	d.group = false
}

func (d *Doc) toggle(r *rec) {
	captured := d.extract(r.off, r.in)
	in := piecesLen(r.out)
	d.splice(r.off, r.in, r.out)
	r.out, r.in = captured, in
	d.group = false
}

// Undo takes back the last step. It returns the place and size of the text
// that came back.
func (d *Doc) Undo() (off, n int64, ok bool) {
	if len(d.undo) == 0 {
		return 0, 0, false
	}
	r := d.undo[len(d.undo)-1]
	d.undo = d.undo[:len(d.undo)-1]
	d.toggle(r)
	d.redo = append(d.redo, r)
	d.state = r.before
	return r.off, r.in, true
}

// Redo does the last undone step again.
func (d *Doc) Redo() (off, n int64, ok bool) {
	if len(d.redo) == 0 {
		return 0, 0, false
	}
	r := d.redo[len(d.redo)-1]
	d.redo = d.redo[:len(d.redo)-1]
	d.toggle(r)
	d.undo = append(d.undo, r)
	d.state = r.after
	return r.off, r.in, true
}

// ClearUndo forgets the undo and redo lists, which also releases the
// buffers that only they still use.
func (d *Doc) ClearUndo() {
	d.undo, d.redo, d.group = nil, nil, false
}

// Chunk returns the bytes from off to the end of the piece that holds off,
// without copying; nil at the end of the document. The result is only valid
// until the next edit.
func (d *Doc) Chunk(off int64) []byte {
	i, a := d.find(off)
	if i >= len(d.pieces) {
		return nil
	}
	p := d.pieces[i]
	return p.buf.data[p.off+a : p.off+p.n]
}

// ReadAt copies the bytes at off into p and returns how many.
func (d *Doc) ReadAt(p []byte, off int64) int {
	n := 0
	for n < len(p) {
		c := d.Chunk(off + int64(n))
		if c == nil {
			break
		}
		n += copy(p[n:], c)
	}
	return n
}

// Bytes returns a copy of the n bytes at off.
func (d *Doc) Bytes(off, n int64) []byte {
	off = max(0, min(off, d.Len()))
	n = max(0, min(n, d.Len()-off))
	out := make([]byte, n)
	d.ReadAt(out, off)
	return out
}

// ByteAt returns the byte at off, or 0 outside the document.
func (d *Doc) ByteAt(off int64) byte {
	if off < 0 {
		return 0
	}
	if c := d.Chunk(off); len(c) > 0 {
		return c[0]
	}
	return 0
}

// window returns the n bytes at off, without copying when they lie in one
// piece and in scratch otherwise.
func (d *Doc) window(off, n int64, scratch *[]byte) []byte {
	if c := d.Chunk(off); int64(len(c)) >= n {
		return c[:n]
	}
	if int64(cap(*scratch)) < n {
		*scratch = make([]byte, n)
	}
	buf := (*scratch)[:n]
	return buf[:d.ReadAt(buf, off)]
}

// LineStart returns the offset of a line, counted from 0. Lines past the
// end give the start of the last line.
func (d *Doc) LineStart(line int64) int64 {
	if line <= 0 {
		return 0
	}
	line = min(line, d.Lines()-1)
	i := sort.Search(len(d.pieces), func(i int) bool { return d.cumBrk[i+1] >= line })
	p := d.pieces[i]
	return d.cumLen[i] + p.buf.nth(p.off, p.off+p.n, line-d.cumBrk[i]) - p.off
}

// LineOf returns the number of the line, counted from 0, that holds off.
func (d *Doc) LineOf(off int64) int64 {
	off = max(0, min(off, d.Len()))
	i, a := d.find(off)
	if a == 0 {
		return d.cumBrk[i]
	}
	p := d.pieces[i]
	return d.cumBrk[i] + p.buf.breaks(p.off, p.off+a)
}

func isBreak(c byte) bool { return c == '\n' || c == '\r' }

// LineBounds returns, for the line that holds off, where it starts, where
// its text ends and where the next line starts (end and next are the same
// on the last line). An offset at a line break belongs to the line before.
func (d *Doc) LineBounds(off int64) (start, end, next int64) {
	off = max(0, min(off, d.Len()))
	if off > 0 && d.ByteAt(off) == '\n' && d.ByteAt(off-1) == '\r' {
		off-- // between CR and LF
	}
	// Look back for the line break before off, a short way first.
	start = -1
	var buf [4096]byte
	for _, size := range [...]int64{1, 256, 4096} {
		lo := max(0, off-size)
		w := buf[:d.ReadAt(buf[:off-lo], lo)]
		for k := len(w) - 1; k >= 0; k-- {
			if isBreak(w[k]) {
				start = lo + int64(k) + 1
				break
			}
		}
		if start < 0 && lo == 0 {
			start = 0
		}
		if start >= 0 {
			break
		}
	}
	line := int64(-1)
	if start < 0 { // a long line: use the index
		line = d.LineOf(off)
		start = d.LineStart(line)
	}
	// Look ahead for the end of the line.
	pos := off
	for pos-off < blockSize {
		c := d.Chunk(pos)
		if c == nil {
			return start, d.Len(), d.Len()
		}
		if len(c) > 4096 {
			c = c[:4096]
		}
		for k, ch := range c {
			if isBreak(ch) {
				end = pos + int64(k)
				next = end + 1
				if ch == '\r' && d.ByteAt(next) == '\n' {
					next++
				}
				return start, end, next
			}
		}
		pos += int64(len(c))
	}
	if line < 0 {
		line = d.LineOf(start)
	}
	if line >= d.Lines()-1 {
		return start, d.Len(), d.Len()
	}
	next = d.LineStart(line + 1)
	end = next - 1
	if d.ByteAt(end) == '\n' && end > start && d.ByteAt(end-1) == '\r' {
		end--
	}
	return start, end, next
}
