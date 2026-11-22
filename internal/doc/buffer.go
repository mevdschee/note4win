// Package doc is the text document of text4win: a piece table over the
// bytes of the file, which stay where they are (in a memory mapped file for
// huge files), with a sparse line index. The text is UTF-8; it is never
// normalized, so every byte that is not edited is saved back unchanged.
//
// A line break is CR LF, LF or CR. Pieces count their own breaks, which only
// adds up when no piece boundary lies between a CR and its LF; every edit
// keeps that invariant (see fix).
package doc

import (
	"bytes"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
)

// blockSize is the distance between two entries of a buffer's line index.
const blockSize = 64 << 10

var (
	crSep   = []byte{'\r'}
	lfSep   = []byte{'\n'}
	crlfSep = []byte{'\r', '\n'}
)

// buffer is a run of bytes that pieces point into. It is immutable, except
// for the add buffer of a document, which only grows.
type buffer struct {
	data []byte
	// idx[i] is the number of marks in data[:i*blockSize], where a mark is a
	// CR, or an LF that does not follow a CR. It is nil for small buffers and
	// for the add buffer, which are scanned instead.
	idx []int64
}

// newBuffer indexes data. Progress is called with the number of bytes done,
// from several goroutines; stop cancels (the buffer is useless then).
func newBuffer(data []byte, progress func(done int64), stop *atomic.Bool) *buffer {
	b := &buffer{data: data}
	n := int64(len(data))
	if n < 4*blockSize {
		return b
	}
	blocks := int((n + blockSize - 1) / blockSize)
	counts := make([]int64, blocks)
	var next, done atomic.Int64
	var wg sync.WaitGroup
	const batch = 64
	for w := 0; w < runtime.NumCPU(); w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for {
				first := int(next.Add(batch)) - batch
				if first >= blocks || (stop != nil && stop.Load()) {
					return
				}
				last := min(first+batch, blocks)
				for i := first; i < last; i++ {
					counts[i] = b.scan(int64(i)*blockSize, min(int64(i+1)*blockSize, n))
				}
				if progress != nil {
					progress(done.Add(int64(last-first) * blockSize))
				}
			}
		}()
	}
	wg.Wait()
	b.idx = make([]int64, blocks+1)
	for i, c := range counts {
		b.idx[i+1] = b.idx[i] + c
	}
	return b
}

// scan counts the marks in data[s:e].
func (b *buffer) scan(s, e int64) int64 {
	if s >= e {
		return 0
	}
	seg := b.data[s:e]
	cr := bytes.Count(seg, crSep)
	lf := bytes.Count(seg, lfSep)
	if lf == 0 {
		return int64(cr)
	}
	pairs := 0
	if cr > 0 {
		pairs = bytes.Count(seg, crlfSep)
	}
	if s > 0 && b.data[s-1] == '\r' && seg[0] == '\n' {
		pairs++
	}
	return int64(cr + lf - pairs)
}

// prefix is the number of marks in data[:i].
func (b *buffer) prefix(i int64) int64 {
	if b.idx == nil {
		return b.scan(0, i)
	}
	blk := i / blockSize
	return b.idx[blk] + b.scan(blk*blockSize, i)
}

// splitPair reports whether s lies between a CR and its LF.
func (b *buffer) splitPair(s int64) bool {
	return s > 0 && s < int64(len(b.data)) && b.data[s-1] == '\r' && b.data[s] == '\n'
}

// breaks counts the line breaks of data[s:e] seen on its own: an LF at s
// counts even when a CR precedes it in the buffer.
func (b *buffer) breaks(s, e int64) int64 {
	if s >= e {
		return 0
	}
	var n int64
	if b.idx == nil || e-s <= 2*blockSize {
		n = b.scan(s, e)
	} else {
		n = b.prefix(e) - b.prefix(s)
	}
	if b.splitPair(s) {
		n++
	}
	return n
}

// nth returns the position just after the k-th (1-based) line break of
// data[s:e], which must have at least k breaks.
func (b *buffer) nth(s, e, k int64) int64 {
	d := b.data
	j, own := s, s // an LF at own counts even after a CR
	if b.idx != nil && e-s > 2*blockSize {
		t := b.prefix(s) + k // the mark to find, counted from the buffer start
		if b.splitPair(s) {
			t--
		}
		blk := int64(sort.Search(len(b.idx), func(i int) bool { return b.idx[i] >= t })) - 1
		if start := blk * blockSize; start > s {
			j, own, k = start, -1, t-b.idx[blk]
		}
	}
	for ; j < e; j++ {
		c := d[j]
		if c == '\n' {
			if j != own && j > 0 && d[j-1] == '\r' {
				continue
			}
		} else if c != '\r' {
			continue
		}
		if k--; k == 0 {
			if c == '\r' && j+1 < e && d[j+1] == '\n' {
				return j + 2
			}
			return j + 1
		}
	}
	return e
}
