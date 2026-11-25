//go:build windows

package ui

// The binary mode of the editor: the bytes of the document in rows of 16,
// each with its address, the bytes in hex and the bytes as characters, like
// a hex editor. A row starts at a multiple of 16, so the scroll position and
// the rows are plain arithmetic and the size of the file does not matter.
//
// The caret is at a byte, in the hex column or in the character column (Tab
// changes that). Typing overwrites bytes (OVR) or inserts them (INS); the
// Insert key changes that. In OVR mode the size of the file only changes by
// typing or pasting at its end.

import (
	"errors"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/mevdschee/note4win/internal/doc"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

// Hex reports whether the editor is in binary mode.
func (e *Editor) Hex() bool { return e.hex }

// Overwrite reports whether typing in binary mode replaces bytes.
func (e *Editor) Overwrite() bool { return e.ovr }

// SetHex turns the binary mode on or off. The document stays the same.
func (e *Editor) SetHex(on bool) {
	if e.hex == on {
		return
	}
	e.hex = on
	e.nibble, e.hexChars = 0, false
	if !on {
		e.anchor, e.caret = e.snap(e.anchor), e.snap(e.caret)
	}
	e.caretEOL, e.wantX, e.xoff, e.maxW = false, -1, 0, 0
	e.d.BreakUndo()
	if e.Hwnd() == 0 {
		return
	}
	e.SetFont(e.lf, e.zoom)
	e.reveal()
	e.notify()
}

// HexColumn reports whether the caret of the binary mode is in the hex
// column, and not in the character column.
func (e *Editor) HexColumn() bool { return !e.hexChars }

// GoTo puts the caret at off and shows it.
func (e *Editor) GoTo(off int64) {
	if !e.hex {
		off = e.snap(off)
	}
	e.caretEOL = false
	e.moveTo(off, false)
}

// SetOverwrite chooses between overwriting and inserting in binary mode.
func (e *Editor) SetOverwrite(ovr bool) {
	e.ovr = ovr
	e.nibble = 0
	if e.hasFocus && e.Hwnd() != 0 {
		e.createCaret()
		e.updateCaret()
	}
	e.notify()
}

// snap moves an offset out of the middle of a character or a CR LF, where
// the caret of the text mode cannot be.
func (e *Editor) snap(off int64) int64 {
	if off <= 0 || off >= e.d.Len() {
		return max(0, min(off, e.d.Len()))
	}
	if e.d.ByteAt(off) == '\n' && e.d.ByteAt(off-1) == '\r' {
		return off - 1
	}
	if utf8.RuneStart(e.d.ByteAt(off)) {
		return off
	}
	var b [utf8.UTFMax]byte
	for back := int64(1); back < utf8.UTFMax && back <= off; back++ {
		if !utf8.RuneStart(e.d.ByteAt(off - back)) {
			continue
		}
		n := e.d.ReadAt(b[:], off-back)
		if _, size := utf8.DecodeRune(b[:n]); int64(size) > back {
			return off - back
		}
		break
	}
	return off
}

// fixedFont creates a font and selects it in hdc; a font with characters of
// different widths is replaced by Consolas of the same size, because the
// columns of the binary mode need equal widths. It returns the font and
// what was selected before.
func fixedFont(hdc uintptr, lf logFont) (font, old uintptr) {
	font = createFont(&lf)
	old, _, _ = procSelectObject.Call(hdc, font)
	var tm textMetric
	procGetTextMetricsW.Call(hdc, uintptr(unsafe.Pointer(&tm)))
	if tm.PitchAndFamily&0x01 != 0 { // TMPF_FIXED_PITCH, which is set for the others
		lf.setFace("Consolas")
		lf.Pitch = 0x31 // FIXED_PITCH | FF_MODERN
		lf.Italic = 0
		fixed := createFont(&lf)
		procSelectObject.Call(hdc, fixed)
		deleteObject(font)
		font = fixed
	}
	return font, old
}

// ---- rows ----

func hexRowStart(off int64) int64 { return off &^ (hexCols - 1) }

// hexLastRow is the start of the last row: the one that shows the end of
// the document, where the caret can be, also when it holds no bytes.
func (e *Editor) hexLastRow() int64 { return hexRowStart(e.d.Len()) }

func (e *Editor) hexMoveRows(off int64, n int) int64 {
	return max(0, min(hexRowStart(off)+int64(n)*hexCols, e.hexLastRow()))
}

func (e *Editor) hexDigits() int { return hexAddrDigits(e.d.Len()) }

// hexCaretCell is the cell of the caret in its row.
func (e *Editor) hexCaretCell() int {
	k := int(e.caret - hexRowStart(e.caret))
	if e.hexChars {
		return chrCol(e.hexDigits(), k)
	}
	return hexCol(e.hexDigits(), k) + e.nibble
}

func (e *Editor) hexCaretPoint() (x, y int32, visible bool) {
	row := hexRowStart(e.caret)
	if row < e.top || (row-e.top)/hexCols >= int64(e.ch/e.lineH)+1 {
		return 0, 0, false
	}
	y = int32((row-e.top)/hexCols) * e.lineH
	return int32(e.hexCaretCell())*e.avgW + e.margin - e.xoff, y, y < e.ch
}

func (e *Editor) hexReveal() {
	row, page := hexRowStart(e.caret), int64(e.pageRows())
	top := e.top
	switch {
	case row < top:
		top = row
	case row >= top+page*hexCols:
		top = row - (page-1)*hexCols
	}
	changed := false
	if top != e.top {
		e.top = min(top, e.lastTop())
		changed = true
	}
	cx := int32(e.hexCaretCell()) * e.avgW
	room := e.cw - 2*e.margin
	switch {
	case cx < e.xoff:
		e.xoff = max(0, cx-room/4)
		changed = true
	case cx+e.avgW > e.xoff+room:
		e.xoff = cx + e.avgW - room + min(room/4, 8*e.avgW)
		changed = true
	}
	if changed {
		e.Hwnd().InvalidateRect(nil, false)
	}
	e.updateScrollBars()
	e.updateCaret()
}

// hexAt tells what is at a point of the client area: the byte, whether it
// is in the character column, which of its hex digits, and the nearest
// place between two bytes, which is where a selection ends. Area picks the
// column: 0 for hex, 1 for characters, -1 for the one at the point.
func (e *Editor) hexAt(px, py int32, area int) (off int64, chars bool, nibble int, edge int64) {
	row := e.hexMoveRows(e.top, int(max(0, py)/e.lineH))
	if py < 0 {
		row = e.hexMoveRows(e.top, -1)
	}
	digits := e.hexDigits()
	cx := px + e.xoff - e.margin
	chars = area == 1 || area < 0 && cx >= int32(chrCol(digits, 0)-1)*e.avgW
	var k, ek int64
	if chars {
		rel := max(0, cx-int32(chrCol(digits, 0))*e.avgW)
		k = int64(rel / e.avgW)
		ek = int64((rel + e.avgW/2) / e.avgW)
	} else {
		rel := max(0, cx-int32(hexCol(digits, 0))*e.avgW)
		k = int64(rel / (3 * e.avgW))
		ek = int64((rel + 2*e.avgW) / (3 * e.avgW))
		if k < hexCols && rel-int32(k)*3*e.avgW >= e.avgW {
			nibble = 1
		}
	}
	off = row + min(k, hexCols-1)
	if off >= e.d.Len() {
		off, nibble = e.d.Len(), 0
	}
	return off, chars, nibble, min(row+min(ek, hexCols), e.d.Len())
}

func (e *Editor) hexButtonDown(p ui.WmMouse) {
	pt := p.Pos()
	off, chars, nibble, edge := e.hexAt(pt.X, pt.Y, -1)
	e.hexChars = chars
	e.Hwnd().SetCapture()
	e.dragging = true
	if p.HasShift() {
		e.moveTo(edge, true)
		return
	}
	e.moveTo(off, false)
	e.nibble = nibble
	e.updateCaret()
}

// ---- painting ----

// mix returns a colour between a and b: parts of 8 from b.
func mix(a, b uint32, parts uint32) uint32 {
	var out uint32
	for shift := 0; shift < 24; shift += 8 {
		ca, cb := (a>>shift)&0xFF, (b>>shift)&0xFF
		out |= ((ca*(8-parts) + cb*parts) / 8) << shift
	}
	return out
}

// drawHexRow draws the row that starts at off into the row bitmap.
func (e *Editor) drawHexRow(off, selA, selB int64) {
	full := win.RECT{Right: e.cw, Bottom: e.lineH}
	fillRect(e.memDC, &full, e.colors.bg)
	var b [hexCols]byte
	n := int64(e.d.ReadAt(b[:], off))
	digits := e.hexDigits()
	e.hexText = hexRow(e.hexText[:0], digits, off, b[:n])
	text := e.hexText
	if cap(e.dx) < len(text) {
		e.dx = make([]int32, len(text), max(len(text), segSize+8))
	}
	dx := e.dx[:len(text)]
	for i := range dx {
		dx[i] = e.avgW
	}
	origin := e.margin - e.xoff
	cells := func(from, to int) win.RECT {
		return win.RECT{Left: origin + int32(from)*e.avgW, Right: origin + int32(to)*e.avgW, Bottom: e.lineH}
	}
	out := func(from int, color uint32, clip *win.RECT) {
		var opts uintptr
		if clip != nil {
			opts = etoClipped
		}
		procSetTextColor.Call(e.memDC, uintptr(color))
		procExtTextOutW.Call(e.memDC, uintptr(origin+int32(from)*e.avgW), 0, opts, uintptr(unsafe.Pointer(clip)),
			uintptr(unsafe.Pointer(&text[from])), uintptr(len(text)-from), uintptr(unsafe.Pointer(&dx[from])))
	}

	// The byte of the caret is marked in the column where the caret is not.
	if k := int(e.caret - off); selA == selB && k >= 0 && k < int(n) {
		rc := cells(chrCol(digits, k), chrCol(digits, k)+1)
		if e.hexChars {
			rc = cells(hexCol(digits, k), hexCol(digits, k)+2)
		}
		fillRect(e.memDC, &rc, mix(e.colors.bg, e.colors.fg, 2))
	}
	addr := cells(0, digits)
	out(0, mix(e.colors.bg, e.colors.fg, 4), &addr)
	out(digits, e.colors.fg, nil)

	// The selection, in both columns.
	a, z := int(max(selA, off)-off), int(min(selB, off+n)-off)
	if a >= z {
		return
	}
	for _, rc := range []win.RECT{
		cells(hexCol(digits, a), hexCol(digits, z-1)+2),
		cells(chrCol(digits, a), chrCol(digits, z)),
	} {
		fillRect(e.memDC, &rc, e.colors.selBg)
		out(digits, e.colors.selFg, &rc)
	}
}

// ---- keyboard ----

func (e *Editor) hexKey(vk co.VK) {
	ctrl, shift := keyDown(co.VK_CONTROL), keyDown(co.VK_SHIFT)
	if keyDown(co.VK_MENU) {
		return
	}
	selA, selB := e.Selection()
	n := e.d.Len()
	row := hexRowStart(e.caret)
	switch vk {
	case co.VK_LEFT:
		switch {
		case !shift && e.HasSelection():
			e.moveTo(selA, false)
		case !shift && e.nibble == 1:
			e.moveTo(e.caret, false)
		default:
			e.moveTo(e.caret-1, shift)
		}
	case co.VK_RIGHT:
		if !shift && e.HasSelection() {
			e.moveTo(selB, false)
		} else {
			e.moveTo(e.caret+1, shift)
		}
	case co.VK_UP:
		if ctrl {
			e.scrollRows(-1)
		} else if e.caret >= hexCols {
			e.moveTo(e.caret-hexCols, shift)
		}
	case co.VK_DOWN:
		if ctrl {
			e.scrollRows(1)
		} else if row+hexCols <= n {
			e.moveTo(min(e.caret+hexCols, n), shift)
		}
	case co.VK_PRIOR, co.VK_NEXT:
		rows := int64(max(1, e.pageRows()-1))
		if vk == co.VK_PRIOR {
			rows = -min(rows, row/hexCols)
		} else {
			rows = min(rows, (e.hexLastRow()-row)/hexCols)
		}
		e.top = min(e.hexMoveRows(e.top, int(rows)), e.lastTop())
		e.moveTo(min(e.caret+rows*hexCols, n), shift)
		e.Hwnd().InvalidateRect(nil, false)
	case co.VK_HOME:
		if ctrl {
			e.moveTo(0, shift)
		} else {
			e.moveTo(row, shift)
		}
	case co.VK_END:
		switch {
		case ctrl:
			e.moveTo(n, shift)
		case shift:
			e.moveTo(min(row+hexCols, n), true)
		default:
			e.moveTo(min(row+hexCols-1, n), false)
		}
	case co.VK_TAB:
		if !ctrl {
			e.hexChars = !e.hexChars
			e.moveTo(e.caret, true)
		}
	case co.VK_BACK:
		switch {
		case e.HasSelection():
			e.Delete()
		case e.caret == 0:
		case e.ovr:
			e.moveTo(e.caret-1, false)
		default:
			e.replace(e.caret-1, e.caret, nil, doc.Backspace)
		}
	case co.VK_DELETE:
		switch {
		case shift && e.HasSelection():
			e.Cut()
		case e.ovr || e.HasSelection():
			e.Delete()
		default:
			e.replace(e.caret, min(e.caret+1, n), nil, doc.Delete)
		}
	case co.VK_INSERT:
		switch {
		case ctrl:
			e.Copy()
		case shift:
			e.Paste()
		default:
			e.SetOverwrite(!e.ovr)
		}
	case co.VK('A'):
		if ctrl {
			e.SelectAll()
		}
	case co.VK('C'):
		if ctrl {
			e.Copy()
		}
	case co.VK('X'):
		if ctrl {
			e.Cut()
		}
	case co.VK('V'):
		if ctrl {
			e.Paste()
		}
	case co.VK('Z'):
		if ctrl && shift {
			e.Redo()
		} else if ctrl {
			e.Undo()
		}
	case co.VK('Y'):
		if ctrl {
			e.Redo()
		}
	}
}

func (e *Editor) hexChar(r rune) {
	if r < 0x20 || r == 0x7F {
		return
	}
	if utf16.IsSurrogate(r) {
		if r < 0xDC00 {
			e.highSurr = r
			return
		}
		r = utf16.DecodeRune(e.highSurr, r)
		e.highSurr = 0
	}
	if e.hexChars {
		e.hexType(utf8.AppendRune(nil, r))
		return
	}
	v, ok := hexVal(r)
	if !ok {
		beep()
		return
	}
	off := e.hexStart()
	switch {
	case e.nibble == 1 && off < e.d.Len():
		e.hexPut(off, 1, []byte{e.d.ByteAt(off)&0xF0 | v}, off+1, 0)
	case e.ovr && off < e.d.Len():
		e.hexPut(off, 1, []byte{e.d.ByteAt(off)&0x0F | v<<4}, off, 1)
	default:
		e.hexPut(off, 0, []byte{v << 4}, off, 1)
	}
}

// hexType types bytes in the character column.
func (e *Editor) hexType(text []byte) {
	off := e.hexStart()
	del := int64(0)
	if e.ovr {
		del = min(int64(len(text)), e.d.Len()-off)
	}
	e.hexPut(off, del, text, off+int64(len(text)), 0)
}

// hexStart prepares for typing with a selection: in INS mode the selection
// goes, in OVR mode typing starts at its first byte. It returns the caret.
func (e *Editor) hexStart() int64 {
	a, b := e.Selection()
	if a == b {
		return e.caret
	}
	e.d.BreakUndo()
	if !e.ovr {
		e.d.Replace(a, b-a, nil, doc.Other)
		e.edited(a, b-a, 0)
	}
	e.anchor, e.caret, e.nibble = a, a, 0
	return a
}

// hexPut replaces del bytes at off by typing, which is undone together
// with the typing before it.
func (e *Editor) hexPut(off, del int64, text []byte, caret int64, nibble int) {
	e.d.Replace(off, del, text, doc.Overtype)
	e.edited(off, del, int64(len(text)))
	e.anchor, e.caret, e.nibble = caret, caret, nibble
	e.finishEdit()
}

// ---- clipboard ----

// maxHexCopy is the most bytes that are copied as hex digits.
const maxHexCopy = maxClipboard / 8

// copyHex puts the bytes a..b on the clipboard as text: pairs of hex digits
// with blanks between them.
func (e *Editor) copyHex(a, b int64) error {
	units := (b - a) * 3 // the last blank is the end of the text
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(units*2))
	if h == 0 {
		return errors.New("There is not enough memory to copy the selection.")
	}
	p, _, _ := procGlobalLock.Call(h)
	out := unsafe.Slice(*(**uint16)(unsafe.Pointer(&p)), units)
	buf := make([]byte, 1<<16)
	i := 0
	for off := a; off < b; {
		n := e.d.ReadAt(buf[:min(int64(len(buf)), b-off)], off)
		if n == 0 {
			break
		}
		for _, c := range buf[:n] {
			out[i], out[i+1], out[i+2] = uint16(hexDigits[c>>4]), uint16(hexDigits[c&15]), ' '
			i += 3
		}
		off += int64(n)
	}
	out[max(0, i-1)] = 0
	procGlobalUnlock.Call(h)
	return e.setClipboard(h)
}

// hexPaste pastes the clipboard: in the hex column text that is written as
// hex digits gives those bytes, everything else its own bytes (UTF-8). In
// OVR mode it goes over the bytes from the caret on.
func (e *Editor) hexPaste(text []byte) {
	if !e.hexChars {
		if b, ok := parseHex(text); ok {
			text = b
		}
	}
	a, b := e.Selection()
	if e.ovr {
		b = min(a+int64(len(text)), e.d.Len())
	}
	e.replace(a, b, text, doc.Other)
}

// FindText is the text that the Find dialog starts with: the selection,
// when it is short and without line breaks. In the hex column of the binary
// mode that is the selection in hex digits.
func (e *Editor) FindText() string {
	a, b := e.Selection()
	switch {
	case !e.hex:
		if sel := e.SelectedText(1000); !containsBreak(sel) {
			return sel
		}
	case a == b || b-a > 64:
	case !e.hexChars:
		return formatHex(e.d.Bytes(a, b-a))
	default:
		sel := e.d.Bytes(a, b-a)
		for _, c := range sel {
			if c < 0x20 || c > 0x7E {
				return ""
			}
		}
		return string(sel)
	}
	return ""
}

func containsBreak(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] == '\r' || s[i] == '\n' {
			return true
		}
	}
	return false
}
