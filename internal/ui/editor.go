//go:build windows

package ui

// The editor control. Notepad uses the Windows edit control, which keeps the
// whole text in memory as UTF-16 and breaks down on large files. This one
// draws the text of a doc.Doc itself and only ever looks at the part that is
// on screen:
//
//   - A line is cut in segments of at most segSize bytes; only lines longer
//     than that have more than one. A segment is the unit that is decoded,
//     measured and wrapped (a layout), so the work per row has a limit, also
//     in a file that is one line of a gigabyte.
//   - A row on screen is identified by the byte offset of its first
//     character. The scroll position is the offset of the top row, and the
//     vertical scrollbar works in bytes, not in lines or rows: nothing
//     depends on the number of rows of the whole document, which is unknown
//     when lines wrap.

import (
	"sort"
	"unicode/utf8"
	"unsafe"

	"github.com/mevdschee/text4win/internal/doc"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

// segSize is the most bytes of a line that are laid out as one piece. A
// line without word wrap continues on the next row after this many bytes,
// like lines longer than 1024 characters do in the Notepad of old.
const segSize = 8192

// seg is a part of a line.
type seg struct {
	start, end int64 // the text
	next       int64 // start of the next segment; past the line break for the last one
	index      int64 // number of the segment in its line
	ls, le, ln int64 // the line: start, end of text, start of next line
}

// layout is a segment prepared for the screen.
type layout struct {
	s    seg
	text []uint16 // UTF-16, as drawn
	boff []int32  // byte offset in the segment of each unit, and of the end
	xs   []int32  // x of each unit in its row
	rows []int32  // first unit of each row
	ends []int32  // x of the end of each row
}

func (l *layout) rowEnd(r int) int {
	if r+1 < len(l.rows) {
		return int(l.rows[r+1])
	}
	return len(l.text)
}

// rowOfUnit returns the row that shows unit u.
func (l *layout) rowOfUnit(u int) int {
	return sort.Search(len(l.rows), func(i int) bool { return int(l.rows[i]) > u }) - 1
}

// unitAt returns the unit at a byte offset in the document.
func (l *layout) unitAt(off int64) int {
	rel := int32(max(0, min(off-l.s.start, l.s.end-l.s.start)))
	return sort.Search(len(l.text), func(i int) bool { return l.boff[i] >= rel })
}

func (l *layout) offsetOf(u int) int64 { return l.s.start + int64(l.boff[u]) }

// x returns the position of unit u when it is shown in row r.
func (l *layout) x(r, u int) int32 {
	if u >= l.rowEnd(r) {
		return l.ends[r]
	}
	return l.xs[u]
}

// colors of the editor.
type editorColors struct{ bg, fg, selBg, selFg uint32 }

// Editor is the text area.
type Editor struct {
	ctl *ui.Control
	d   *doc.Doc
	eol doc.EOL // what Enter inserts

	// OnChange is called after the text, the selection or the zoom changed.
	OnChange func()
	// OnContextMenu shows the right-click menu at a screen position.
	OnContextMenu func(pt win.POINT)
	// OnZoom is called for Ctrl+wheel, with the direction.
	OnZoom func(dir int)

	lf       logFont // the font at 100%
	zoom     int     // percent
	font     uintptr
	lineH    int32
	avgW     int32
	tabW     int32
	margin   int32
	wrap     bool
	colors   editorColors
	memDC    uintptr
	memBmp   uintptr
	memW     int32
	oldBmp   uintptr
	cw, ch   int32 // client size
	hasFocus bool
	locked   bool // no input, while a file loads

	top    int64 // offset of the first row on screen
	xoff   int32 // horizontal scroll position
	maxW   int32 // widest row seen, without word wrap
	maxTop int64 // largest value for top, -1 when unknown
	shift  uint  // vertical scrollbar unit is 1<<shift bytes

	anchor, caret int64
	caretEOL      bool  // show the caret at the end of the row above
	wantX         int32 // column that Up and Down aim for, -1 when unset

	cache map[int64]*layout

	dragging  bool
	wheelAcc  int
	highSurr  rune
	col       colCache
	dx        []int32
	scratch   []byte
	clickTime uint32
}

type colCache struct {
	state        *doc.Doc
	ls, off, col int64
}

const timerAutoScroll = 1

// NewEditor creates the editor as a child of parent.
func NewEditor(parent ui.Parent) *Editor {
	e := &Editor{zoom: 100, maxTop: -1, wantX: -1, cache: map[int64]*layout{}}
	e.d = doc.New(nil, nil, nil)
	e.colors = editorColors{
		bg: sysColor(co.COLOR_WINDOW), fg: sysColor(co.COLOR_WINDOWTEXT),
		selBg: sysColor(co.COLOR_HIGHLIGHT), selFg: sysColor(co.COLOR_HIGHLIGHTTEXT),
	}
	e.ctl = ui.NewControl(parent, ui.OptsControl().
		ClassCursor(win.HCURSOR(loadCursor(idcIBeam))).
		ClassBrush(win.HBRUSH(0)).
		ClassStyle(co.CS_DBLCLKS).
		Style(co.WS_CHILD|co.WS_VISIBLE|co.WS_TABSTOP|co.WS_VSCROLL|co.WS_HSCROLL).
		ExStyle(co.WS_EX_LEFT))
	e.events()
	return e
}

// Hwnd returns the window of the editor.
func (e *Editor) Hwnd() win.HWND { return e.ctl.Hwnd() }

// Doc returns the document that is shown.
func (e *Editor) Doc() *doc.Doc { return e.d }

// SetDoc shows another document, with the caret at its start.
func (e *Editor) SetDoc(d *doc.Doc, eol doc.EOL) {
	e.d, e.eol = d, eol
	e.top, e.xoff, e.maxW = 0, 0, 0
	e.anchor, e.caret, e.caretEOL, e.wantX = 0, 0, false, -1
	e.relayout()
}

// SetEOL sets the line break that Enter inserts.
func (e *Editor) SetEOL(eol doc.EOL) { e.eol = eol }

// SetLocked blocks or allows input.
func (e *Editor) SetLocked(locked bool) { e.locked = locked }

// Selection returns the selected range, start before end.
func (e *Editor) Selection() (int64, int64) {
	return min(e.anchor, e.caret), max(e.anchor, e.caret)
}

// HasSelection reports whether text is selected.
func (e *Editor) HasSelection() bool { return e.anchor != e.caret }

// Caret returns the offset of the caret.
func (e *Editor) Caret() int64 { return e.caret }

// Wrap reports whether word wrap is on.
func (e *Editor) Wrap() bool { return e.wrap }

// Zoom returns the zoom level in percent.
func (e *Editor) Zoom() int { return e.zoom }

// SetColors sets the colours and repaints.
func (e *Editor) SetColors(c editorColors) {
	e.colors = c
	if e.Hwnd() != 0 {
		e.Hwnd().InvalidateRect(nil, false)
	}
}

// SetFont sets the font (at 100% zoom) and the zoom level.
func (e *Editor) SetFont(lf logFont, zoom int) {
	e.lf, e.zoom = lf, zoom
	if e.Hwnd() == 0 {
		return
	}
	deleteObject(e.font)
	scaled := lf
	scaled.Height = int32(mulDiv(int(lf.Height), zoom, 100))
	if scaled.Height == 0 {
		scaled.Height = -1
	}
	scaled.Width = 0
	e.font = createFont(&scaled)
	procSelectObject.Call(e.memDC, e.font)
	var tm textMetric
	procGetTextMetricsW.Call(e.memDC, uintptr(unsafe.Pointer(&tm)))
	e.lineH = max(1, tm.Height+tm.ExternalLeading)
	e.avgW = max(1, tm.AveCharWidth)
	e.tabW = 8 * e.avgW
	e.margin = int32(ui.DpiX(4))
	e.maxW = 0
	if e.hasFocus {
		e.createCaret()
	}
	e.relayout()
}

// SetWrap turns word wrap on or off.
func (e *Editor) SetWrap(wrap bool) {
	e.wrap = wrap
	if e.Hwnd() == 0 {
		return
	}
	e.xoff = 0
	procShowScrollBar.Call(uintptr(e.Hwnd()), uintptr(co.SBB_HORZ), boolArg(!wrap))
	e.relayout()
}

func boolArg(b bool) uintptr {
	if b {
		return 1
	}
	return 0
}

// relayout throws away everything that was measured and repaints.
func (e *Editor) relayout() {
	if e.Hwnd() == 0 || e.font == 0 {
		return
	}
	clear(e.cache)
	e.maxTop = -1
	e.top = e.rowStart(e.top)
	e.clampTop()
	e.updateScrollBars()
	e.updateCaret()
	e.Hwnd().InvalidateRect(nil, false)
}

func (e *Editor) events() {
	on := e.ctl.On()
	on.WmCreate(func(_ ui.WmCreate) int {
		e.memDC, _, _ = procCreateCompatibleDC.Call(0)
		procSetBkMode.Call(e.memDC, 1) // TRANSPARENT
		rc, _ := e.Hwnd().GetClientRect()
		e.cw, e.ch = rc.Right, rc.Bottom
		e.SetFont(e.lf, e.zoom)
		e.SetWrap(e.wrap)
		return 0
	})
	on.WmDestroy(func() {
		if e.oldBmp != 0 {
			procSelectObject.Call(e.memDC, e.oldBmp)
		}
		deleteObject(e.memBmp)
		procDeleteDC.Call(e.memDC)
		deleteObject(e.font)
	})
	on.WmSize(func(p ui.WmSize) {
		sz := p.ClientAreaSize()
		if sz.Cx == e.cw && sz.Cy == e.ch {
			return
		}
		widthChanged := sz.Cx != e.cw
		e.cw, e.ch = sz.Cx, sz.Cy
		if e.font == 0 {
			return
		}
		if e.wrap && widthChanged {
			e.relayout()
			return
		}
		e.maxTop = -1
		e.clampTop()
		e.updateScrollBars()
		e.updateCaret()
	})
	on.WmEraseBkgnd(func(_ ui.WmEraseBkgnd) int { return 1 })
	on.WmPaint(e.paint)
	on.WmGetDlgCode(func(_ ui.WmGetDlgCode) co.DLGC {
		return co.DLGC_WANTALLKEYS | co.DLGC_WANTCHARS | co.DLGC_WANTARROWS | co.DLGC_WANTTAB
	})
	on.WmSetFocus(func(_ ui.WmSetFocus) {
		e.hasFocus = true
		e.createCaret()
		e.updateCaret()
	})
	on.WmKillFocus(func(_ ui.WmKillFocus) {
		e.hasFocus = false
		win.DestroyCaret()
	})
	on.WmVScroll(e.onVScroll)
	on.WmHScroll(e.onHScroll)
	on.Wm(wmMouseWheel, func(p ui.Wm) uintptr {
		e.onWheel(int(int16(p.WParam>>16)), p.WParam&0x0008 != 0, p.WParam&0x0004 != 0)
		return 0
	})
	on.Wm(wmMouseHWheel, func(p ui.Wm) uintptr {
		e.scrollX(int32(int(int16(p.WParam>>16)) * int(e.avgW) * 3 / 120))
		return 0
	})
	e.inputEvents()
}

func (e *Editor) createCaret() {
	width := uint32(1)
	procSystemParametersInfoW.Call(spiGetCaretWidth, 0, uintptr(unsafe.Pointer(&width)), 0)
	procCreateCaret.Call(uintptr(e.Hwnd()), 0, uintptr(max(1, width)), uintptr(e.lineH))
	e.Hwnd().ShowCaret()
}

// ---- segments and layouts ----

// cut returns the start of segment i of a line: about i*segSize bytes into
// it, but after a blank when there is one nearby, so that word wrap does not
// break a word there, and never inside a character.
func (e *Editor) cut(ls, le, i int64) int64 {
	p := ls + i*segSize
	if i <= 0 {
		return ls
	}
	if p >= le {
		return le
	}
	var buf [256]byte
	lo := p - int64(len(buf))
	e.d.ReadAt(buf[:], lo)
	for k := len(buf) - 1; k > 0; k-- {
		if isSpace(buf[k-1]) {
			return lo + int64(k)
		}
	}
	for k := 0; k < utf8.UTFMax-1 && p > ls && !utf8.RuneStart(e.d.ByteAt(p)); k++ {
		p--
	}
	return p
}

func (e *Editor) segN(ls, le, ln, i int64) seg {
	n := max(1, (le-ls+segSize-1)/segSize)
	s := seg{start: e.cut(ls, le, i), end: e.cut(ls, le, i+1), index: i, ls: ls, le: le, ln: ln}
	s.next = s.end
	if i >= n-1 {
		s.end, s.next = le, ln
	}
	return s
}

// segAt returns the segment that holds off.
func (e *Editor) segAt(off int64) seg {
	ls, le, ln := e.d.LineBounds(off)
	if le-ls <= segSize {
		return seg{start: ls, end: le, next: ln, ls: ls, le: le, ln: ln}
	}
	n := (le - ls + segSize - 1) / segSize
	i := max(0, min((off-ls)/segSize, n-1))
	if i+1 < n && off >= e.cut(ls, le, i+1) {
		i++
	}
	return e.segN(ls, le, ln, i)
}

func (e *Editor) nextSeg(s seg) (seg, bool) {
	if s.next != s.ln {
		return e.segN(s.ls, s.le, s.ln, s.index+1), true
	}
	if s.le == s.ln { // the last line
		return seg{}, false
	}
	return e.segAt(s.ln), true
}

func (e *Editor) prevSeg(s seg) (seg, bool) {
	if s.index > 0 {
		return e.segN(s.ls, s.le, s.ln, s.index-1), true
	}
	if s.ls == 0 {
		return seg{}, false
	}
	ls, le, ln := e.d.LineBounds(s.ls - 1)
	return e.segN(ls, le, ln, max(1, (le-ls+segSize-1)/segSize)-1), true
}

// wrapWidth is the room for text in a row.
func (e *Editor) wrapWidth() int32 { return max(e.avgW, e.cw-2*e.margin) }

func (e *Editor) layoutOf(s seg) *layout {
	if l, ok := e.cache[s.start]; ok && l.s == s {
		return l
	}
	if len(e.cache) > 1024 {
		clear(e.cache)
	}
	l := e.build(s)
	e.cache[s.start] = l
	return l
}

func (e *Editor) layoutAt(off int64) *layout { return e.layoutOf(e.segAt(off)) }

func isLowSurrogate(c uint16) bool  { return c >= 0xDC00 && c < 0xE000 }
func isHighSurrogate(c uint16) bool { return c >= 0xD800 && c < 0xDC00 }

// build decodes, measures and wraps a segment.
func (e *Editor) build(s seg) *layout {
	n := int(s.end - s.start)
	if cap(e.scratch) < n {
		e.scratch = make([]byte, n, max(n, segSize+8))
	}
	b := e.scratch[:n]
	b = b[:e.d.ReadAt(b, s.start)]
	l := &layout{s: s, text: make([]uint16, 0, len(b)), boff: make([]int32, 0, len(b)+1)}
	for i := 0; i < len(b); {
		r, size := rune(b[i]), 1
		if r >= utf8.RuneSelf {
			r, size = utf8.DecodeRune(b[i:])
		}
		switch {
		case r >= 0x10000:
			r -= 0x10000
			l.text = append(l.text, uint16(0xD800+(r>>10)), uint16(0xDC00+(r&0x3FF)))
			l.boff = append(l.boff, int32(i), int32(i))
		case r == 0:
			l.text = append(l.text, ' ') // like Notepad
			l.boff = append(l.boff, int32(i))
		default:
			l.text = append(l.text, uint16(r))
			l.boff = append(l.boff, int32(i))
		}
		i += size
	}
	l.boff = append(l.boff, int32(len(b)))
	units := len(l.text)

	// Measure the runs between the tabs.
	if cap(e.dx) < units+1 {
		e.dx = make([]int32, units+1, max(units+1, segSize+8))
	}
	w := e.dx[:units+1]
	for i := 0; i < units; {
		if l.text[i] == '\t' {
			w[i] = 0
			i++
			continue
		}
		j := i
		for j < units && l.text[j] != '\t' {
			j++
		}
		var sz size
		procGetTextExtentExPointW.Call(e.memDC, uintptr(unsafe.Pointer(&l.text[i])), uintptr(j-i), 0, 0,
			uintptr(unsafe.Pointer(&w[i])), uintptr(unsafe.Pointer(&sz)))
		for k := j - 1; k > i; k-- { // from running totals to widths
			w[k] -= w[k-1]
		}
		i = j
	}

	// Place the units, on a new row when the width is used up.
	l.xs = make([]int32, units+1)
	l.rows = []int32{0}
	limit := e.wrapWidth()
	var x int32
	rowStart, breakAt := 0, -1
	for i := 0; i < units; i++ {
		c := l.text[i]
		cw := w[i]
		blank := c == ' ' || c == '\t'
		if c == '\t' {
			cw = (x/e.tabW+1)*e.tabW - x
		}
		if e.wrap && x+cw > limit && i > rowStart && !blank {
			br := i
			if breakAt > rowStart {
				br = breakAt
			} else if isLowSurrogate(c) && i-1 > rowStart {
				br = i - 1
			}
			end := x
			if br < i {
				end = l.xs[br]
			}
			l.ends = append(l.ends, end)
			l.rows = append(l.rows, int32(br))
			rowStart, breakAt, x = br, -1, 0
			i = br - 1
			continue
		}
		l.xs[i] = x
		x += cw
		if blank {
			breakAt = i + 1
		}
	}
	l.xs[units] = x
	l.ends = append(l.ends, x)
	if !e.wrap && x > e.maxW {
		e.maxW = x
	}
	return l
}

// ---- rows ----

// place returns the layout, row and x where an offset is shown. With eol
// set, an offset at the start of a wrapped row is shown at the end of the
// row above.
func (e *Editor) place(off int64, eol bool) (*layout, int, int32) {
	l := e.layoutAt(off)
	u := l.unitAt(off)
	r := l.rowOfUnit(u)
	if eol && int(l.rows[r]) == u {
		if r > 0 {
			return l, r - 1, l.ends[r-1]
		}
		if l.s.index > 0 {
			if ps, ok := e.prevSeg(l.s); ok {
				pl := e.layoutOf(ps)
				last := len(pl.rows) - 1
				return pl, last, pl.ends[last]
			}
		}
	}
	return l, r, l.x(r, u)
}

// rowStart returns the offset of the first character of the row that shows off.
func (e *Editor) rowStart(off int64) int64 {
	l, r, _ := e.place(off, false)
	return l.offsetOf(int(l.rows[r]))
}

// softRowStart reports whether off starts a row that continues a line.
func (e *Editor) softRowStart(off int64) bool {
	l := e.layoutAt(off)
	u := l.unitAt(off)
	r := l.rowOfUnit(u)
	return int(l.rows[r]) == u && (r > 0 || l.s.index > 0)
}

// nextRow returns the start of the row after the row that starts at off.
func (e *Editor) nextRow(off int64) (int64, bool) {
	l, r, _ := e.place(off, false)
	if r+1 < len(l.rows) {
		return l.offsetOf(int(l.rows[r+1])), true
	}
	if ns, ok := e.nextSeg(l.s); ok {
		return ns.start, true
	}
	return off, false
}

func (e *Editor) prevRow(off int64) (int64, bool) {
	l, r, _ := e.place(off, false)
	if r > 0 {
		return l.offsetOf(int(l.rows[r-1])), true
	}
	if ps, ok := e.prevSeg(l.s); ok {
		pl := e.layoutOf(ps)
		return pl.offsetOf(int(pl.rows[len(pl.rows)-1])), true
	}
	return off, false
}

// moveRows returns the row start n rows from the row that starts at off.
func (e *Editor) moveRows(off int64, n int) int64 {
	for ; n > 0; n-- {
		next, ok := e.nextRow(off)
		if !ok {
			break
		}
		off = next
	}
	for ; n < 0; n++ {
		prev, ok := e.prevRow(off)
		if !ok {
			break
		}
		off = prev
	}
	return off
}

// pageRows is the number of rows that fit completely.
func (e *Editor) pageRows() int { return max(1, int(e.ch/max(1, e.lineH))) }

// lastTop returns the largest scroll position: the one that shows the end
// of the document in the last row.
func (e *Editor) lastTop() int64 {
	if e.maxTop < 0 {
		e.maxTop = e.moveRows(e.rowStart(e.d.Len()), -(e.pageRows() - 1))
	}
	return e.maxTop
}

func (e *Editor) clampTop() {
	if e.top > e.lastTop() {
		e.top = e.lastTop()
	}
}

// hit returns the offset at a point in the client area, and whether the
// caret belongs at the end of the row above for it.
func (e *Editor) hit(px, py int32) (int64, bool) {
	row := e.top
	for i := int32(0); i < max(0, py)/e.lineH; i++ {
		next, ok := e.nextRow(row)
		if !ok {
			break
		}
		row = next
	}
	if py < 0 {
		row = e.moveRows(e.top, -1)
	}
	return e.hitRow(row, px+e.xoff-e.margin)
}

// hitRow returns the offset at x in the row that starts at off.
func (e *Editor) hitRow(off int64, x int32) (int64, bool) {
	l, r, _ := e.place(off, false)
	lo, hi := int(l.rows[r]), l.rowEnd(r)
	u := lo + sort.Search(hi-lo, func(i int) bool { return l.xs[lo+i] > x })
	// u is the first unit that starts right of x; x is in the one before.
	if u > lo {
		right := l.ends[r]
		if u < hi {
			right = l.xs[u]
		}
		if x-l.xs[u-1] < right-x {
			u--
		}
	}
	for u > lo && u < hi && isLowSurrogate(l.text[u]) {
		u++
	}
	return l.offsetOf(u), u == hi && r+1 < len(l.rows)
}

// ---- painting ----

func (e *Editor) paint() {
	var ps win.PAINTSTRUCT
	hdcW, err := e.Hwnd().BeginPaint(&ps)
	if err != nil {
		return
	}
	defer e.Hwnd().EndPaint(&ps)
	hdc := uintptr(hdcW)
	if e.font == 0 || e.cw <= 0 {
		return
	}
	if e.memBmp == 0 || e.memW < e.cw {
		if e.memBmp != 0 {
			procSelectObject.Call(e.memDC, e.oldBmp)
			deleteObject(e.memBmp)
		}
		e.memW = e.cw + 64
		e.memBmp, _, _ = procCreateCompatibleBitmap.Call(hdc, uintptr(e.memW), uintptr(e.lineH))
		e.oldBmp, _, _ = procSelectObject.Call(e.memDC, e.memBmp)
	}
	selA, selB := e.Selection()
	row, more := e.top, true
	for y := int32(0); y < e.ch; y += e.lineH {
		visible := y+e.lineH > ps.RcPaint.Top && y < ps.RcPaint.Bottom
		if !more {
			if visible {
				rc := win.RECT{Left: 0, Top: y, Right: e.cw, Bottom: e.ch}
				fillRect(hdc, &rc, e.colors.bg)
			}
			break
		}
		if visible {
			e.drawRow(row, selA, selB)
			procBitBlt.Call(hdc, 0, uintptr(y), uintptr(e.cw), uintptr(e.lineH), e.memDC, 0, 0, srcCopy)
		}
		row, more = e.nextRow(row)
	}
}

// drawRow draws the row that starts at off into the row bitmap.
func (e *Editor) drawRow(off, selA, selB int64) {
	full := win.RECT{Right: e.cw, Bottom: e.lineH}
	fillRect(e.memDC, &full, e.colors.bg)
	l, r, _ := e.place(off, false)
	lo, hi := int(l.rows[r]), l.rowEnd(r)
	origin := e.margin - e.xoff

	// The units that are in view.
	first := lo + sort.Search(hi-lo, func(i int) bool { return l.xs[lo+i]+origin > 0 })
	if first > lo {
		first--
	}
	for first > lo && isLowSurrogate(l.text[first]) {
		first--
	}
	last := lo + sort.Search(hi-lo, func(i int) bool { return l.xs[lo+i]+origin >= e.cw })
	for last < hi && isLowSurrogate(l.text[last]) {
		last++
	}

	procSetTextColor.Call(e.memDC, uintptr(e.colors.fg))
	e.drawRuns(l, first, last, origin, nil)

	// The selection: its background, and the text again in its own colour.
	rowA, rowB := l.offsetOf(lo), l.offsetOf(hi)
	if selA == selB || selB < rowA || selA > rowB {
		return
	}
	a, b := lo, hi
	if selA > rowA {
		a = l.unitAt(selA)
	}
	if selB < rowB {
		b = l.unitAt(selB)
	}
	rc := win.RECT{Left: l.x(r, a) + origin, Right: l.x(r, b) + origin, Bottom: e.lineH}
	lastRow := r == len(l.rows)-1 && l.s.next == l.s.ln
	if b == hi && lastRow && selB > l.s.end && l.s.le != l.s.ln {
		rc.Right += e.avgW // the line break is selected
	} else if b == hi && !lastRow && selB > rowB {
		rc.Right = max(rc.Right, min(e.cw, l.ends[r]+origin))
	}
	if rc.Right <= rc.Left {
		return
	}
	rc.Left, rc.Right = max(rc.Left, 0), min(rc.Right, e.cw)
	fillRect(e.memDC, &rc, e.colors.selBg)
	procSetTextColor.Call(e.memDC, uintptr(e.colors.selFg))
	e.drawRuns(l, max(first, a), min(last, b), origin, &rc)
}

// drawRuns draws the units from..to of a row, the text between tabs in one
// call each.
func (e *Editor) drawRuns(l *layout, from, to int, origin int32, clip *win.RECT) {
	var opts uintptr
	if clip != nil {
		opts = etoClipped
	}
	for i := from; i < to; {
		if l.text[i] == '\t' {
			i++
			continue
		}
		j := i
		for j < to && l.text[j] != '\t' {
			j++
		}
		procExtTextOutW.Call(e.memDC, uintptr(l.xs[i]+origin), 0, opts, uintptr(unsafe.Pointer(clip)),
			uintptr(unsafe.Pointer(&l.text[i])), uintptr(j-i), 0)
		i = j
	}
}

// ---- scrolling ----

func (e *Editor) updateScrollBars() {
	h := e.Hwnd()
	var si win.SCROLLINFO
	si.SetCbSize()
	si.Mask = co.SIF_RANGE | co.SIF_PAGE | co.SIF_POS | co.SIF_DISABLENOSCROLL
	total, last := e.d.Len(), e.lastTop()
	if last > 0 {
		e.shift = 0
		for total>>e.shift >= 1<<30 {
			e.shift++
		}
		si.Max = int32(total >> e.shift)
		si.Page = uint32(max(1, si.Max-int32(last>>e.shift)+1))
		si.Pos = int32(e.top >> e.shift)
		if e.top >= last {
			si.Pos = si.Max
		}
	}
	h.SetScrollInfo(co.SBB_VERT, &si, true)
	if e.wrap {
		return
	}
	si = win.SCROLLINFO{}
	si.SetCbSize()
	si.Mask = co.SIF_RANGE | co.SIF_PAGE | co.SIF_POS | co.SIF_DISABLENOSCROLL
	si.Max = e.maxW + 2*e.margin + e.avgW
	si.Page = uint32(max(1, e.cw))
	e.xoff = max(0, min(e.xoff, si.Max-e.cw+1))
	si.Pos = e.xoff
	h.SetScrollInfo(co.SBB_HORZ, &si, true)
}

// setTop scrolls to a row start.
func (e *Editor) setTop(top int64) {
	top = min(top, e.lastTop())
	if top == e.top {
		return
	}
	e.top = top
	e.updateScrollBars()
	e.updateCaret()
	e.Hwnd().InvalidateRect(nil, false)
}

func (e *Editor) scrollRows(n int) { e.setTop(e.moveRows(e.top, n)) }

func (e *Editor) scrollX(dx int32) {
	if e.wrap {
		return
	}
	old := e.xoff
	e.xoff = max(0, e.xoff+dx)
	e.updateScrollBars() // clamps
	if e.xoff != old {
		e.updateCaret()
		e.Hwnd().InvalidateRect(nil, false)
	}
}

func (e *Editor) trackPos(bar co.SBB) int32 {
	var si win.SCROLLINFO
	si.SetCbSize()
	si.Mask = co.SIF_TRACKPOS
	e.Hwnd().GetScrollInfo(bar, &si)
	return si.TrackPos
}

func (e *Editor) onVScroll(p ui.WmScroll) {
	switch p.Request() {
	case co.SB_REQ_LINEUP:
		e.scrollRows(-1)
	case co.SB_REQ_LINEDOWN:
		e.scrollRows(1)
	case co.SB_REQ_PAGEUP:
		e.scrollRows(-max(1, e.pageRows()-1))
	case co.SB_REQ_PAGEDOWN:
		e.scrollRows(max(1, e.pageRows()-1))
	case co.SB_REQ_TOP:
		e.setTop(0)
	case co.SB_REQ_BOTTOM:
		e.setTop(e.lastTop())
	case co.SB_REQ_THUMBTRACK, co.SB_REQ_THUMBPOSITION:
		pos := int64(e.trackPos(co.SBB_VERT)) << e.shift
		if pos >= e.lastTop() {
			e.setTop(e.lastTop())
		} else {
			e.setTop(e.rowStart(pos))
		}
	}
}

func (e *Editor) onHScroll(p ui.WmScroll) {
	switch p.Request() {
	case co.SB_REQ_LINELEFT:
		e.scrollX(-e.avgW)
	case co.SB_REQ_LINERIGHT:
		e.scrollX(e.avgW)
	case co.SB_REQ_PAGELEFT:
		e.scrollX(-max(e.avgW, e.cw-2*e.avgW))
	case co.SB_REQ_PAGERIGHT:
		e.scrollX(max(e.avgW, e.cw-2*e.avgW))
	case co.SB_REQ_THUMBTRACK, co.SB_REQ_THUMBPOSITION:
		e.scrollX(e.trackPos(co.SBB_HORZ) - e.xoff)
	}
}

func (e *Editor) onWheel(delta int, ctrl, shift bool) {
	if ctrl {
		if e.OnZoom != nil && delta != 0 {
			e.OnZoom(delta / max(1, abs(delta)))
		}
		return
	}
	lines := uint32(3)
	procSystemParametersInfoW.Call(spiGetWheelLines, 0, uintptr(unsafe.Pointer(&lines)), 0)
	if shift {
		e.scrollX(int32(-delta * int(e.avgW) * 3 / 120))
		return
	}
	if lines == 0xFFFFFFFF { // a page per notch
		lines = uint32(max(1, e.pageRows()-1))
	}
	e.wheelAcc += delta * int(lines)
	n := e.wheelAcc / 120
	e.wheelAcc -= n * 120
	if n != 0 {
		e.scrollRows(-n)
	}
}

func abs(v int) int {
	if v < 0 {
		return -v
	}
	return v
}

// ---- caret ----

// caretPoint returns the client position of the caret.
func (e *Editor) caretPoint() (x, y int32, visible bool) {
	l, r, cx := e.place(e.caret, e.caretEOL)
	target := l.offsetOf(int(l.rows[r]))
	if target < e.top {
		return 0, 0, false
	}
	row := e.top
	for y = 0; y < e.ch; y += e.lineH {
		if row == target {
			return cx + e.margin - e.xoff, y, true
		}
		next, ok := e.nextRow(row)
		if !ok {
			break
		}
		row = next
	}
	return 0, 0, false
}

func (e *Editor) updateCaret() {
	if !e.hasFocus || e.font == 0 {
		return
	}
	x, y, visible := e.caretPoint()
	if !visible || x < 0 || x > e.cw {
		win.SetCaretPos(-100, -100)
		return
	}
	win.SetCaretPos(int(x), int(y))
	// Tell the input method where the composition goes.
	if himc, _, _ := procImmGetContext.Call(uintptr(e.Hwnd())); himc != 0 {
		form := struct {
			style uint32
			pt    win.POINT
			area  win.RECT
		}{style: 0x0002, pt: win.POINT{X: x, Y: y}} // CFS_POINT
		procImmSetCompositionWnd.Call(himc, uintptr(unsafe.Pointer(&form)))
		procImmReleaseContext.Call(uintptr(e.Hwnd()), himc)
	}
}

// reveal scrolls so that the caret is in view.
func (e *Editor) reveal() {
	l, r, cx := e.place(e.caret, e.caretEOL)
	target := l.offsetOf(int(l.rows[r]))
	top := e.top
	if target < top {
		top = target
	} else {
		// Is it within a page of the top?
		row, found := e.top, false
		for i := 0; i < e.pageRows(); i++ {
			if row == target {
				found = true
				break
			}
			next, ok := e.nextRow(row)
			if !ok {
				break
			}
			row = next
		}
		if !found {
			top = e.moveRows(target, -(e.pageRows() - 1))
		}
	}
	changed := false
	if top != e.top {
		e.top = min(top, e.lastTop())
		changed = true
	}
	if !e.wrap {
		room := e.cw - 2*e.margin
		switch {
		case cx < e.xoff:
			e.xoff = max(0, cx-room/4)
			changed = true
		case cx > e.xoff+room:
			e.xoff = cx - room + min(room/4, 8*e.avgW)
			changed = true
		}
	}
	if changed {
		e.Hwnd().InvalidateRect(nil, false)
	}
	e.updateScrollBars()
	e.updateCaret()
}

// LineCol returns the line and column of the caret, counted from 1. The
// column counts UTF-16 units, like Notepad does.
func (e *Editor) LineCol() (int64, int64) {
	line := e.d.LineOf(e.caret)
	ls, _, _ := e.d.LineBounds(e.caret)
	from, col := ls, int64(0)
	if c := &e.col; c.state == e.d && c.ls == ls && c.off <= e.caret {
		from, col = c.off, c.col
	}
	var buf [4096]byte
	for from < e.caret {
		n := e.d.ReadAt(buf[:min(int64(len(buf)), e.caret-from)], from)
		if n == 0 {
			break
		}
		for _, b := range buf[:n] {
			if b&0xC0 != 0x80 { // not a continuation byte
				col++
			}
			if b >= 0xF0 { // two units
				col++
			}
		}
		from += int64(n)
	}
	e.col = colCache{state: e.d, ls: ls, off: e.caret, col: col}
	return line + 1, col + 1
}
