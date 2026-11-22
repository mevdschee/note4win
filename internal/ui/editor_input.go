//go:build windows

package ui

import (
	"errors"
	"unicode/utf16"
	"unicode/utf8"
	"unsafe"

	"github.com/mevdschee/text4win/internal/doc"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

// scanLimit is how far the word commands look for the end of a word.
const scanLimit = 1 << 20

func (e *Editor) inputEvents() {
	on := e.ctl.On()
	on.WmKeyDown(func(p ui.WmKey) {
		if !e.locked {
			e.onKey(p.VirtualKeyCode())
		}
	})
	on.WmChar(func(p ui.WmChar) {
		if !e.locked {
			e.onChar(p.CharCode())
		}
	})
	on.WmLButtonDown(func(p ui.WmMouse) {
		e.Hwnd().SetFocus()
		if e.locked {
			return
		}
		pt := p.Pos()
		off, eol := e.hit(pt.X, pt.Y)
		e.Hwnd().SetCapture()
		e.dragging = true
		e.caretEOL = eol
		e.moveTo(off, p.HasShift())
	})
	on.WmLButtonDblClk(func(p ui.WmMouse) {
		if e.locked {
			return
		}
		pt := p.Pos()
		off, _ := e.hit(pt.X, pt.Y)
		a, b := e.wordAt(off)
		e.anchor = a
		e.caretEOL = false
		e.moveTo(b, true)
	})
	on.WmMouseMove(func(p ui.WmMouse) {
		if !e.dragging {
			return
		}
		pt := p.Pos()
		if pt.Y < 0 || pt.Y >= e.ch {
			e.Hwnd().SetTimer(timerAutoScroll, 50)
		} else {
			e.Hwnd().KillTimer(timerAutoScroll)
		}
		e.dragTo(pt)
	})
	on.WmTimer(timerAutoScroll, func() {
		pt, err := win.GetCursorPos()
		if err != nil || !e.dragging {
			e.Hwnd().KillTimer(timerAutoScroll)
			return
		}
		e.Hwnd().ScreenToClientPt(&pt)
		switch {
		case pt.Y < 0:
			e.scrollRows(-1 - int(-pt.Y/(2*e.lineH)))
		case pt.Y >= e.ch:
			e.scrollRows(1 + int((pt.Y-e.ch)/(2*e.lineH)))
		}
		e.dragTo(pt)
	})
	on.WmLButtonUp(func(_ ui.WmMouse) {
		if e.dragging {
			win.ReleaseCapture()
		}
	})
	on.WmCaptureChanged(func(_ ui.WmCaptureChanged) {
		e.dragging = false
		e.Hwnd().KillTimer(timerAutoScroll)
	})
	on.WmContextMenu(func(p ui.WmContextMenu) {
		if e.locked || e.OnContextMenu == nil {
			return
		}
		pt := p.CursorPos()
		if pt.X == -1 && pt.Y == -1 { // from the keyboard: at the caret
			x, y, _ := e.caretPoint()
			pt = win.POINT{X: max(0, x), Y: max(0, y) + e.lineH}
			e.Hwnd().ClientToScreenPt(&pt)
		} else {
			client := pt
			e.Hwnd().ScreenToClientPt(&client)
			if client.X >= e.cw || client.Y >= e.ch {
				e.Hwnd().DefWindowProc(co.WM_CONTEXTMENU, win.WPARAM(e.Hwnd()), win.MAKELPARAM(uint16(pt.X), uint16(pt.Y)))
				return // a scrollbar
			}
			if off, _ := e.hit(client.X, client.Y); !e.HasSelection() || off < min(e.anchor, e.caret) || off > max(e.anchor, e.caret) {
				e.moveTo(off, false)
			}
		}
		e.OnContextMenu(pt)
	})
}

func (e *Editor) dragTo(pt win.POINT) {
	y := max(0, min(pt.Y, e.ch-1))
	off, eol := e.hit(pt.X, y)
	if off != e.caret {
		e.caretEOL = eol
		e.moveTo(off, true)
	}
}

// moveTo puts the caret at off, selecting from the anchor when extend is set.
func (e *Editor) moveTo(off int64, extend bool) {
	off = max(0, min(off, e.d.Len()))
	had := e.HasSelection()
	e.caret = off
	if !extend {
		e.anchor = off
	}
	e.wantX = -1
	e.d.BreakUndo()
	if had || e.HasSelection() {
		e.Hwnd().InvalidateRect(nil, false)
	}
	e.reveal()
	e.notify()
}

// Select selects a range and scrolls to it.
func (e *Editor) Select(a, b int64) {
	e.anchor = max(0, min(a, e.d.Len()))
	e.caretEOL = false
	e.caret = e.anchor
	e.reveal() // first the start, so that a match shows from its beginning
	e.moveTo(b, true)
	e.Hwnd().InvalidateRect(nil, false)
}

func (e *Editor) notify() {
	if e.OnChange != nil {
		e.OnChange()
	}
}

// ---- positions ----

// nextChar returns the offset after the character at off; a CR LF is one.
func (e *Editor) nextChar(off int64) int64 {
	var b [4]byte
	n := e.d.ReadAt(b[:], off)
	if n == 0 {
		return off
	}
	if b[0] == '\r' && n > 1 && b[1] == '\n' {
		return off + 2
	}
	_, size := utf8.DecodeRune(b[:n])
	return off + int64(size)
}

func (e *Editor) prevChar(off int64) int64 {
	lo := max(0, off-4)
	var b [4]byte
	n := e.d.ReadAt(b[:off-lo], lo)
	if n == 0 {
		return off
	}
	if n >= 2 && b[n-2] == '\r' && b[n-1] == '\n' {
		return off - 2
	}
	_, size := utf8.DecodeLastRune(b[:n])
	return off - int64(size)
}

func isSpace(c byte) bool { return c == ' ' || c == '\t' }

// class tells word characters, blanks, line breaks and the rest apart.
func class(c byte) int {
	switch {
	case isSpace(c):
		return 0
	case c == '\r' || c == '\n':
		return 1
	}
	return 2
}

// nextWord returns the start of the next word, as Ctrl+Right in an edit
// control: past the rest of this word and the blanks after it.
func (e *Editor) nextWord(off int64) int64 {
	var buf [256]byte
	stage, limit := 0, off+scanLimit
	if c := e.d.ByteAt(off); off < e.d.Len() && class(c) == 1 {
		return e.nextChar(off)
	}
	for off < e.d.Len() && off < limit {
		n := e.d.ReadAt(buf[:], off)
		for _, c := range buf[:n] {
			cl := class(c)
			if stage == 0 && cl != 2 {
				stage = 1
			}
			if stage == 1 && cl != 0 {
				return off
			}
			off++
		}
	}
	return off
}

func (e *Editor) prevWord(off int64) int64 {
	var buf [256]byte
	stage, limit := 0, off-scanLimit
	if off > 0 && class(e.d.ByteAt(off-1)) == 1 {
		return e.prevChar(off)
	}
	for off > 0 && off > limit {
		lo := max(0, off-int64(len(buf)))
		n := e.d.ReadAt(buf[:off-lo], lo)
		for k := n - 1; k >= 0; k-- {
			cl := class(buf[k])
			if stage == 0 && cl != 0 {
				stage = 1
			}
			if stage == 1 && cl != 2 {
				return off
			}
			off--
		}
	}
	return off
}

func isWordByte(c byte) bool {
	return c >= utf8.RuneSelf || c == '_' || '0' <= c && c <= '9' || 'a' <= c|0x20 && c|0x20 <= 'z'
}

// wordAt returns the word around off, for a double click.
func (e *Editor) wordAt(off int64) (int64, int64) {
	ls, le, _ := e.d.LineBounds(off)
	lo, hi := max(ls, off-4096), min(le, off+4096)
	b := e.d.Bytes(lo, hi-lo)
	i := int(off - lo)
	if i >= len(b) {
		i = len(b) - 1
	}
	if i < 0 {
		return off, off
	}
	same := func(c byte) bool { return isWordByte(c) == isWordByte(b[i]) && isSpace(c) == isSpace(b[i]) }
	a, z := i, i+1
	for a > 0 && same(b[a-1]) {
		a--
	}
	for z < len(b) && same(b[z]) {
		z++
	}
	return lo + int64(a), lo + int64(z)
}

// ---- keyboard ----

func (e *Editor) onKey(vk co.VK) {
	ctrl, shift := keyDown(co.VK_CONTROL), keyDown(co.VK_SHIFT)
	if keyDown(co.VK_MENU) {
		return
	}
	selA, selB := e.Selection()
	switch vk {
	case co.VK_LEFT:
		e.caretEOL = false
		switch {
		case ctrl:
			e.moveTo(e.prevWord(e.caret), shift)
		case !shift && e.HasSelection():
			e.moveTo(selA, false)
		default:
			e.moveTo(e.prevChar(e.caret), shift)
		}
	case co.VK_RIGHT:
		e.caretEOL = false
		switch {
		case ctrl:
			e.moveTo(e.nextWord(e.caret), shift)
		case !shift && e.HasSelection():
			e.moveTo(selB, false)
		default:
			e.moveTo(e.nextChar(e.caret), shift)
		}
	case co.VK_UP:
		if ctrl {
			e.scrollRows(-1)
		} else {
			e.moveVert(-1, shift)
		}
	case co.VK_DOWN:
		if ctrl {
			e.scrollRows(1)
		} else {
			e.moveVert(1, shift)
		}
	case co.VK_PRIOR:
		e.movePage(-1, shift)
	case co.VK_NEXT:
		e.movePage(1, shift)
	case co.VK_HOME:
		e.caretEOL = false
		if ctrl {
			e.moveTo(0, shift)
		} else {
			l, r, _ := e.place(e.caret, e.caretEOL)
			e.moveTo(l.offsetOf(int(l.rows[r])), shift)
		}
	case co.VK_END:
		if ctrl {
			e.caretEOL = false
			e.moveTo(e.d.Len(), shift)
		} else {
			l, r, _ := e.place(e.caret, e.caretEOL)
			e.caretEOL = r+1 < len(l.rows) || l.s.next != l.s.ln
			e.moveTo(l.offsetOf(l.rowEnd(r)), shift)
		}
	case co.VK_BACK:
		switch {
		case e.HasSelection():
			e.Insert(nil, doc.Other)
		case ctrl:
			e.replace(e.prevWord(e.caret), e.caret, nil, doc.Other)
		default:
			e.replace(e.prevChar(e.caret), e.caret, nil, doc.Backspace)
		}
	case co.VK_DELETE:
		switch {
		case shift && e.HasSelection():
			e.Cut()
		case e.HasSelection():
			e.Insert(nil, doc.Other)
		case ctrl: // to the end of the line, like the edit control
			_, le, _ := e.d.LineBounds(e.caret)
			e.replace(e.caret, le, nil, doc.Other)
		default:
			e.replace(e.caret, e.nextChar(e.caret), nil, doc.Delete)
		}
	case co.VK_INSERT:
		switch {
		case ctrl:
			e.Copy()
		case shift:
			e.Paste()
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

// moveVert moves the caret a row up or down, keeping its column.
func (e *Editor) moveVert(dir int, extend bool) {
	l, r, x := e.place(e.caret, e.caretEOL)
	if e.wantX < 0 {
		e.wantX = x
	}
	want := e.wantX
	row := l.offsetOf(int(l.rows[r]))
	target := e.moveRows(row, dir)
	if target == row {
		return
	}
	off, eol := e.hitRow(target, want)
	e.caretEOL = eol
	e.moveTo(off, extend)
	e.wantX = want
}

func (e *Editor) movePage(dir int, extend bool) {
	l, r, x := e.place(e.caret, e.caretEOL)
	if e.wantX < 0 {
		e.wantX = x
	}
	want := e.wantX
	n := dir * max(1, e.pageRows()-1)
	target := e.moveRows(l.offsetOf(int(l.rows[r])), n)
	e.top = min(e.moveRows(e.top, n), e.lastTop())
	off, eol := e.hitRow(target, want)
	e.caretEOL = eol
	e.moveTo(off, extend)
	e.wantX = want
	e.Hwnd().InvalidateRect(nil, false)
}

func (e *Editor) onChar(r rune) {
	ctrl, alt := keyDown(co.VK_CONTROL), keyDown(co.VK_MENU)
	switch {
	case r == '\r':
		if ctrl {
			return
		}
		e.d.BreakUndo()
		e.Insert(e.eol.Bytes(), doc.Other)
		return
	case r == '\t':
		if ctrl && !alt {
			return // Ctrl+I
		}
	case r < 0x20 || r == 0x7F:
		return // Ctrl+letter, Backspace, Escape
	}
	if utf16.IsSurrogate(r) {
		if r < 0xDC00 {
			e.highSurr = r
			return
		}
		r = utf16.DecodeRune(e.highSurr, r)
		e.highSurr = 0
	}
	kind := doc.Typing
	if e.HasSelection() {
		kind = doc.Other
	}
	e.Insert(utf8.AppendRune(nil, r), kind)
	if r == ' ' { // undo goes back a word at a time
		e.d.BreakUndo()
	}
}

// ---- editing ----

// replace replaces the range a..b with text and puts the caret after it.
func (e *Editor) replace(a, b int64, text []byte, kind doc.Kind) {
	if a == b && len(text) == 0 {
		return
	}
	if kind == doc.Other {
		e.d.BreakUndo()
	}
	e.d.Replace(a, b-a, text, kind)
	e.edited(a, b-a, int64(len(text)))
	e.anchor = a + int64(len(text))
	e.caret = e.anchor
	e.finishEdit()
}

// edited adjusts the scroll position for a change of the text.
func (e *Editor) edited(off, del, ins int64) {
	clear(e.cache)
	e.maxTop = -1
	e.col = colCache{}
	switch {
	case e.top > off+del:
		e.top += ins - del
	case e.top > off:
		e.top = off
	}
}

func (e *Editor) finishEdit() {
	e.caretEOL = false
	e.wantX = -1
	e.top = e.rowStart(min(e.top, e.d.Len()))
	e.clampTop()
	e.Hwnd().InvalidateRect(nil, false)
	e.reveal()
	e.notify()
}

// Insert replaces the selection with text.
func (e *Editor) Insert(text []byte, kind doc.Kind) {
	a, b := e.Selection()
	e.replace(a, b, text, kind)
}

// Refresh repaints after the document was changed from outside the editor,
// with the caret at off.
func (e *Editor) Refresh(off int64) {
	clear(e.cache)
	e.maxTop = -1
	e.col = colCache{}
	e.anchor = max(0, min(off, e.d.Len()))
	e.caret = e.anchor
	e.finishEdit()
}

// Undo takes back the last change.
func (e *Editor) Undo() {
	if off, n, ok := e.d.Undo(); ok {
		e.Refresh(off + n)
	}
}

// Redo does the last undone change again.
func (e *Editor) Redo() {
	if off, n, ok := e.d.Redo(); ok {
		e.Refresh(off + n)
	}
}

// SelectAll selects the whole text, without scrolling.
func (e *Editor) SelectAll() {
	e.anchor, e.caret, e.caretEOL, e.wantX = 0, e.d.Len(), false, -1
	e.d.BreakUndo()
	e.updateCaret()
	e.Hwnd().InvalidateRect(nil, false)
	e.notify()
}

// Cut moves the selection to the clipboard.
func (e *Editor) Cut() {
	if e.HasSelection() && e.Copy() {
		e.Insert(nil, doc.Other)
	}
}

// maxClipboard is the most text, in bytes, that is put on the clipboard.
const maxClipboard = 1 << 30

// Copy puts the selection on the clipboard.
func (e *Editor) Copy() bool {
	a, b := e.Selection()
	if a == b {
		return false
	}
	err := errors.New("The selection is too large for the clipboard.")
	if b-a <= maxClipboard {
		restore := waitCursor()
		err = e.copyRange(a, b)
		restore()
	}
	if err != nil {
		e.Hwnd().MessageBox(err.Error(), appName, co.MB_ICONWARNING)
		return false
	}
	return true
}

func (e *Editor) copyRange(a, b int64) error {
	// Count the UTF-16 units, then convert straight into clipboard memory.
	units := int64(1)
	buf := make([]byte, 1<<16)
	for off := a; off < b; {
		n := e.d.ReadAt(buf[:min(int64(len(buf)), b-off)], off)
		for _, c := range buf[:n] {
			if c&0xC0 != 0x80 {
				units++
			}
			if c >= 0xF0 {
				units++
			}
		}
		off += int64(n)
	}
	h, _, _ := procGlobalAlloc.Call(gmemMoveable, uintptr(units*2))
	if h == 0 {
		return errors.New("There is not enough memory to copy the selection.")
	}
	p, _, _ := procGlobalLock.Call(h)
	out := unsafe.Slice(*(**uint16)(unsafe.Pointer(&p)), units)
	i := 0
	text := e.d.Bytes(a, b-a)
	for len(text) > 0 && i < len(out)-1 {
		r, size := utf8.DecodeRune(text)
		text = text[size:]
		if r1, r2 := utf16.EncodeRune(r); r1 != utf8.RuneError && i < len(out)-2 {
			out[i], out[i+1] = uint16(r1), uint16(r2)
			i += 2
		} else {
			out[i] = uint16(r)
			i++
		}
	}
	out[i] = 0
	procGlobalUnlock.Call(h)
	if r, _, _ := procOpenClipboard.Call(uintptr(e.Hwnd())); r == 0 {
		procGlobalFree.Call(h)
		return errors.New("The clipboard is in use by another program.")
	}
	defer procCloseClipboard.Call()
	procEmptyClipboard.Call()
	if r, _, _ := procSetClipboardData.Call(cfUnicodeText, h); r == 0 {
		procGlobalFree.Call(h)
		return errors.New("The selection could not be put on the clipboard.")
	}
	return nil
}

// CanPaste reports whether the clipboard holds text.
func CanPaste() bool {
	r, _, _ := procIsClipboardFormatAvailable.Call(cfUnicodeText)
	return r != 0
}

// clipboardText returns the text on the clipboard as UTF-8.
func clipboardText(owner win.HWND) ([]byte, bool) {
	if r, _, _ := procOpenClipboard.Call(uintptr(owner)); r == 0 {
		return nil, false
	}
	defer procCloseClipboard.Call()
	h, _, _ := procGetClipboardData.Call(cfUnicodeText)
	if h == 0 {
		return nil, false
	}
	p, _, _ := procGlobalLock.Call(h)
	if p == 0 {
		return nil, false
	}
	defer procGlobalUnlock.Call(h)
	size, _, _ := procGlobalSize.Call(h)
	u := unsafe.Slice(*(**uint16)(unsafe.Pointer(&p)), size/2)
	out := make([]byte, 0, len(u)+len(u)/4)
	for i := 0; i < len(u) && u[i] != 0; i++ {
		r := rune(u[i])
		if utf16.IsSurrogate(r) {
			r2 := rune(utf8.RuneError)
			if i+1 < len(u) {
				r2 = rune(u[i+1])
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
	return out, true
}

// Paste inserts the clipboard text, with the line breaks of the document.
func (e *Editor) Paste() {
	text, ok := clipboardText(e.Hwnd())
	if !ok || len(text) == 0 {
		return
	}
	e.Insert(convertEOL(text, e.eol), doc.Other)
}

// SelectedText returns the selection when it is at most limit bytes.
func (e *Editor) SelectedText(limit int64) string {
	a, b := e.Selection()
	if a == b || b-a > limit {
		return ""
	}
	return string(e.d.Bytes(a, b-a))
}
