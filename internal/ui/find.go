//go:build windows

package ui

// The Find, Replace and Go To Line dialogs. Find and Replace stay open
// while the text is edited, which windigo has no window for, so they are
// built here from plain Win32 controls, laid out in dialog units after the
// templates of the Windows common dialogs (with Notepad's "Wrap around").

import (
	"fmt"
	"strconv"
	"strings"
	"syscall"
	"unsafe"

	"github.com/mevdschee/text4win/internal/doc"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

type findState struct {
	what, with            string
	matchCase, wrapAround bool
	down                  bool
	hex                   bool // binary mode: the text is hex bytes, like "4D 5A"

	dlg      win.HWND
	replace  bool // the dialog is the Replace dialog
	font     uintptr
	focus    win.HWND
	x, y     int32 // where the dialog was, when placed is set
	placed   bool
	baseX    int32 // dialog base units of the font
	baseY    int32
	bkBrush  uintptr
	inNotify bool
}

const (
	idFindWhat = 101 + iota
	idReplaceWith
	idMatchCase
	idWrapAround
	idUp
	idDown
	idReplace
	idReplaceAll
	idHex
	idText
	idFindNext = 1 // IDOK: the Enter key
	idCancel   = 2 // IDCANCEL: the Escape key
)

const findClass = "text4win.find"

var (
	findApp      *App
	findProcPtr  = syscall.NewCallback(findProc)
	findClassReg bool
)

func findProc(hwnd, msg, wParam, lParam uintptr) uintptr {
	a := findApp
	if a != nil && win.HWND(hwnd) == a.find.dlg {
		if r, handled := a.findMessage(co.WM(msg), wParam, lParam); handled {
			return r
		}
	}
	r, _, _ := procDefWindowProcW.Call(hwnd, msg, wParam, lParam)
	return r
}

type wndClassEx struct {
	size       uint32
	style      uint32
	wndProc    uintptr
	clsExtra   int32
	wndExtra   int32
	instance   uintptr
	icon       uintptr
	cursor     uintptr
	background uintptr
	menuName   *uint16
	className  *uint16
	iconSm     uintptr
}

// showFind opens the Find or the Replace dialog, or brings it forward.
func (a *App) showFind(replace bool) {
	f := &a.find
	if a.binary { // what the column of the caret holds
		f.hex = a.ed.HexColumn()
	}
	if sel := a.ed.FindText(); sel != "" {
		f.what = sel
	}
	if f.dlg != 0 {
		if f.replace == replace {
			setWindowText(a.findCtl(idFindWhat), f.what)
			a.showFindMode()
			procSetForegroundWindow.Call(uintptr(f.dlg))
			a.focusFindWhat()
			return
		}
		a.closeFind()
	}
	f.replace = replace
	findApp = a
	hInst, _ := a.wnd.Hwnd().HInstance()
	if !findClassReg {
		wc := wndClassEx{wndProc: findProcPtr, instance: uintptr(hInst), cursor: loadCursor(idcArrow),
			className: utf16Ptr(findClass)}
		wc.size = uint32(unsafe.Sizeof(wc))
		procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc)))
		findClassReg = true
	}

	// The dialog font and its base units.
	msgFont, _, _ := systemFonts()
	f.font = createFont(&msgFont)
	hdc, _ := a.wnd.Hwnd().GetDC()
	old, _, _ := procSelectObject.Call(uintptr(hdc), f.font)
	var tm textMetric
	procGetTextMetricsW.Call(uintptr(hdc), uintptr(unsafe.Pointer(&tm)))
	f.baseX = (textWidth(uintptr(hdc), "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz")/26 + 1) / 2
	f.baseY = tm.Height
	procSelectObject.Call(uintptr(hdc), old)
	a.wnd.Hwnd().ReleaseDC(hdc)

	title, w, h := "Find", int32(236), int32(72)
	if replace {
		title, w, h = "Replace", 230, 94
	}
	const style = co.WS_POPUP | co.WS_CAPTION | co.WS_SYSMENU
	const exStyle = co.WS_EX_DLGMODALFRAME | co.WS_EX_CONTROLPARENT
	rc := win.RECT{Right: f.dluX(w), Bottom: f.dluY(h)}
	win.AdjustWindowRectEx(&rc, style, false, exStyle)
	if !f.placed {
		owner, _ := a.wnd.Hwnd().GetWindowRect()
		f.x, f.y = owner.Left+int32(ui.DpiX(60)), owner.Top+int32(ui.DpiY(120))
	}
	hwnd, _, _ := procCreateWindowExW.Call(uintptr(exStyle), uintptr(unsafe.Pointer(utf16Ptr(findClass))),
		uintptr(unsafe.Pointer(utf16Ptr(title))), uintptr(style),
		uintptr(f.x), uintptr(f.y), uintptr(rc.Right-rc.Left), uintptr(rc.Bottom-rc.Top),
		uintptr(a.wnd.Hwnd()), 0, uintptr(hInst), 0)
	f.dlg = win.HWND(hwnd)
	if hwnd == 0 {
		return
	}

	const (
		tab        = uintptr(co.WS_TABSTOP)
		group      = uintptr(co.WS_GROUP)
		edit       = tab | 0x0080    // ES_AUTOHSCROLL
		check      = tab | 0x0003    // BS_AUTOCHECKBOX
		radio      = uintptr(0x0009) // BS_AUTORADIOBUTTON
		box        = uintptr(0x0007) // BS_GROUPBOX
		defPB      = tab | 0x0001    // BS_DEFPUSHBUTTON
		clientEdge = 0x0200          // WS_EX_CLIENTEDGE
	)
	if replace {
		a.addCtl("STATIC", "Fi&nd what:", 0, 0, 4, 9, 48, 8, 0)
		a.addCtl("EDIT", f.what, edit|group, clientEdge, 54, 7, 114, 12, idFindWhat)
		a.addCtl("STATIC", "Re&place with:", 0, 0, 4, 26, 48, 8, 0)
		a.addCtl("EDIT", f.with, edit|group, clientEdge, 54, 24, 114, 12, idReplaceWith)
		a.addCtl("BUTTON", "He&x bytes", radio|group|tab, 0, 5, 44, 50, 12, idHex)
		a.addCtl("BUTTON", "&Text", radio, 0, 58, 44, 40, 12, idText)
		a.addCtl("BUTTON", "Match &case", check|group, 0, 5, 62, 59, 12, idMatchCase)
		a.addCtl("BUTTON", "Wrap ar&ound", check, 0, 5, 76, 65, 12, idWrapAround)
		a.addCtl("BUTTON", "&Find Next", defPB|group, 0, 174, 4, 50, 14, idFindNext)
		a.addCtl("BUTTON", "&Replace", tab, 0, 174, 21, 50, 14, idReplace)
		a.addCtl("BUTTON", "Replace &All", tab, 0, 174, 38, 50, 14, idReplaceAll)
		a.addCtl("BUTTON", "Cancel", tab, 0, 174, 55, 50, 14, idCancel)
	} else {
		a.addCtl("STATIC", "Fi&nd what:", 0, 0, 4, 8, 42, 8, 0)
		a.addCtl("EDIT", f.what, edit|group, clientEdge, 47, 7, 128, 12, idFindWhat)
		a.addCtl("BUTTON", "He&x bytes", radio|group|tab, 0, 4, 25, 50, 12, idHex)
		a.addCtl("BUTTON", "&Text", radio, 0, 56, 25, 40, 12, idText)
		a.addCtl("BUTTON", "Match &case", check|group, 0, 4, 42, 64, 12, idMatchCase)
		a.addCtl("BUTTON", "Wrap ar&ound", check, 0, 4, 56, 64, 12, idWrapAround)
		a.addCtl("BUTTON", "Direction", box|group, 0, 107, 26, 68, 28, 0)
		a.addCtl("BUTTON", "&Up", radio|group|tab, 0, 111, 38, 25, 12, idUp)
		a.addCtl("BUTTON", "&Down", radio, 0, 138, 38, 35, 12, idDown)
		a.addCtl("BUTTON", "&Find Next", defPB|group, 0, 182, 5, 50, 14, idFindNext)
		a.addCtl("BUTTON", "Cancel", tab, 0, 182, 23, 50, 14, idCancel)
		procCheckRadioButton.Call(hwnd, idUp, idDown, uintptr(map[bool]int{true: idDown, false: idUp}[f.down]))
	}
	procCheckDlgButton.Call(hwnd, idMatchCase, boolArg(f.matchCase))
	procCheckDlgButton.Call(hwnd, idWrapAround, boolArg(f.wrapAround))
	a.enableFindButtons()
	a.showFindMode()
	themeDialog(f.dlg)
	f.dlg.ShowWindow(co.SW_SHOW)
	a.focusFindWhat()
}

// showFindMode shows the choice between hex bytes and text, which only
// the binary mode has, and sets it.
func (a *App) showFindMode() {
	f := &a.find
	if f.dlg == 0 {
		return
	}
	show, id := co.SW_HIDE, idText
	if a.binary {
		show = co.SW_SHOW
	}
	if f.hex {
		id = idHex
	}
	a.findCtl(idHex).ShowWindow(show)
	a.findCtl(idText).ShowWindow(show)
	procCheckRadioButton.Call(uintptr(f.dlg), idHex, idText, uintptr(id))
}

func (f *findState) dluX(v int32) int32 { return v * f.baseX / 4 }
func (f *findState) dluY(v int32) int32 { return v * f.baseY / 8 }

func (a *App) addCtl(class, text string, style, exStyle uintptr, x, y, w, h int32, id uintptr) {
	f := &a.find
	hInst, _ := a.wnd.Hwnd().HInstance()
	c, _, _ := procCreateWindowExW.Call(exStyle, uintptr(unsafe.Pointer(utf16Ptr(class))),
		uintptr(unsafe.Pointer(utf16Ptr(text))), style|uintptr(co.WS_CHILD|co.WS_VISIBLE),
		uintptr(f.dluX(x)), uintptr(f.dluY(y)), uintptr(f.dluX(w)), uintptr(f.dluY(h)),
		uintptr(f.dlg), id, uintptr(hInst), 0)
	win.HWND(c).SendMessage(co.WM_SETFONT, win.WPARAM(f.font), 1)
}

func (a *App) findCtl(id uintptr) win.HWND {
	h, _, _ := procGetDlgItem.Call(uintptr(a.find.dlg), id)
	return win.HWND(h)
}

func (a *App) focusFindWhat() {
	edit := a.findCtl(idFindWhat)
	edit.SetFocus()
	edit.SendMessage(co.EM_SETSEL, 0, ^win.LPARAM(0))
}

func (a *App) enableFindButtons() {
	has := windowText(a.findCtl(idFindWhat)) != ""
	for _, id := range []uintptr{idFindNext, idReplace, idReplaceAll} {
		if c := a.findCtl(id); c != 0 {
			c.EnableWindow(has)
		}
	}
}

// readFind copies the controls of the dialog to the state.
func (a *App) readFind() {
	f := &a.find
	checked := func(id uintptr) bool {
		r, _, _ := procIsDlgButtonChecked.Call(uintptr(f.dlg), id)
		return r == 1
	}
	f.what = windowText(a.findCtl(idFindWhat))
	f.matchCase, f.wrapAround = checked(idMatchCase), checked(idWrapAround)
	if a.binary {
		f.hex = checked(idHex)
	}
	if f.replace {
		f.with = windowText(a.findCtl(idReplaceWith))
		f.down = true
	} else {
		f.down = !checked(idUp)
	}
}

func (a *App) closeFind() {
	f := &a.find
	if f.dlg == 0 {
		return
	}
	a.readFind()
	if rc, err := f.dlg.GetWindowRect(); err == nil {
		f.x, f.y, f.placed = rc.Left, rc.Top, true
	}
	dlg := f.dlg
	f.dlg, f.focus = 0, 0
	dlg.DestroyWindow()
	deleteObject(f.font)
	f.font = 0
	a.ed.Hwnd().SetFocus()
}

func (a *App) findMessage(msg co.WM, wParam, lParam uintptr) (uintptr, bool) {
	f := &a.find
	switch msg {
	case co.WM_COMMAND:
		id, code := uint16(wParam), uint16(wParam>>16)
		if id >= cmdNew && id < cmdLast { // a key of the main window
			a.wnd.Hwnd().SendMessage(co.WM_COMMAND, win.WPARAM(wParam), win.LPARAM(lParam))
			return 0, true
		}
		switch id {
		case idFindWhat:
			if code == 0x0300 { // EN_CHANGE
				a.enableFindButtons()
			}
		case idFindNext:
			a.readFind()
			if f.what != "" {
				a.findNext(f.down)
			}
		case idReplace:
			a.readFind()
			a.replaceOne()
		case idReplaceAll:
			a.readFind()
			a.replaceAll()
		case idCancel:
			a.closeFind()
		}
		return 0, true
	case co.WM_CLOSE:
		a.closeFind()
		return 0, true
	case co.WM_ACTIVATE:
		if uint16(wParam) == 0 { // WA_INACTIVE: remember the control with the focus
			if h := win.GetFocus(); h != 0 && f.dlg.IsChild(h) {
				f.focus = h
			}
		} else if f.focus != 0 {
			f.focus.SetFocus()
		}
		return 0, true
	case co.WM_SETFOCUS:
		if f.focus != 0 {
			f.focus.SetFocus()
		} else {
			a.findCtl(idFindWhat).SetFocus()
		}
		return 0, true
	case co.WM_ERASEBKGND:
		rc, _ := f.dlg.GetClientRect()
		fillRect(wParam, &rc, colors.dialog)
		return 1, true
	case co.WM_CTLCOLORSTATIC, co.WM_CTLCOLORBTN:
		procSetTextColor.Call(wParam, uintptr(colors.text))
		procSetBkColor.Call(wParam, uintptr(colors.dialog))
		return solidBrush(colors.dialog), true
	case co.WM_CTLCOLOREDIT:
		if darkMode {
			procSetTextColor.Call(wParam, uintptr(colors.text))
			procSetBkColor.Call(wParam, uintptr(colors.field))
			return solidBrush(colors.field), true
		}
	}
	return 0, false
}

// findOwner is the window that owns the messages of a search.
func (a *App) findOwner() win.HWND {
	if a.find.dlg != 0 {
		return a.find.dlg
	}
	return a.wnd.Hwnd()
}

func (a *App) notFound() {
	a.findOwner().MessageBox(fmt.Sprintf("Cannot find \"%s\"", a.find.what), appName, co.MB_ICONINFORMATION)
}

// findAgain is Find Next and Find Previous of the menu.
func (a *App) findAgain(down bool) {
	if a.find.dlg != 0 {
		a.readFind()
	}
	if a.find.what == "" {
		a.showFind(false)
		return
	}
	a.findNext(down)
}

// needle returns what is searched for, what replaces it and whether case
// matters. With "Hex bytes" chosen in binary mode the text of the dialog
// is hex digits: "4D 5A" stands for those two bytes. It says so and
// returns false when the text is something else.
func (a *App) needle(replacing bool) (what, with string, matchCase, ok bool) {
	f := &a.find
	if !a.binary || !f.hex {
		return f.what, f.with, f.matchCase, true
	}
	w, ok := parseHex([]byte(f.what))
	var r []byte
	if ok && replacing && strings.TrimSpace(f.with) != "" {
		r, ok = parseHex([]byte(f.with))
	}
	if !ok {
		a.findOwner().MessageBox("Hex bytes are written as pairs of hex digits, like 4D 5A.\n\nChoose Text to look for the characters themselves.",
			appName, co.MB_ICONINFORMATION)
		return "", "", false, false
	}
	return string(w), string(r), true, true
}

// search looks for the next match from the selection, going around the end
// of the document when that is set.
func (a *App) search(what string, matchCase, down bool) (doc.Match, bool) {
	f := &a.find
	d := a.ed.Doc()
	selA, selB := a.ed.Selection()
	restore := waitCursor()
	defer restore()
	from := selB
	if !down {
		from = selA
	}
	m, ok := d.Find(what, from, down, matchCase)
	if !ok && f.wrapAround {
		from = 0
		if !down {
			from = d.Len()
		}
		m, ok = d.Find(what, from, down, matchCase)
	}
	return m, ok
}

func (a *App) findNext(down bool) bool {
	what, _, matchCase, ok := a.needle(false)
	if !ok {
		return false
	}
	m, ok := a.search(what, matchCase, down)
	if !ok {
		a.notFound()
		return false
	}
	a.ed.Select(m.Off, m.Off+m.N)
	return true
}

// replaceOne replaces the selection when it is a match and selects the
// next match.
func (a *App) replaceOne() {
	f := &a.find
	if f.what == "" {
		return
	}
	what, with, matchCase, ok := a.needle(true)
	if !ok {
		return
	}
	d := a.ed.Doc()
	selA, selB := a.ed.Selection()
	if selA != selB {
		if m, ok := d.Find(what, selA, true, matchCase); ok && m.Off == selA && m.N == selB-selA {
			a.ed.Insert([]byte(with), doc.Other)
		}
	}
	a.findNext(true)
}

// replaceAll replaces every match in the document, as one undo step.
func (a *App) replaceAll() {
	f := &a.find
	if f.what == "" {
		return
	}
	what, with, matchCase, ok := a.needle(true)
	if !ok {
		return
	}
	restore := waitCursor()
	d := a.ed.Doc()
	matches := d.FindAll(what, matchCase, nil)
	if len(matches) > 0 {
		d.ReplaceAll(matches, []byte(with))
	}
	restore()
	if len(matches) == 0 {
		a.notFound()
		return
	}
	a.ed.top = 0
	a.ed.Refresh(0)
}

// goTo shows the Go To Line dialog, which asks for an address in binary
// mode: decimal, or hex after "0x".
func (a *App) goTo() {
	if a.binary {
		a.goToOffset()
		return
	}
	wnd := newModal(a.wnd, ui.OptsModal().Title("Go To Line").Size(ui.Dpi(250, 104)))
	ui.NewStatic(wnd, ui.OptsStatic().Text("&Line number:").Position(ui.Dpi(12, 12)).Size(ui.Dpi(226, 18)))
	line, _ := a.ed.LineCol()
	edit := ui.NewEdit(wnd, ui.OptsEdit().Text(strconv.FormatInt(line, 10)).Position(ui.Dpi(12, 34)).
		Width(ui.DpiX(226)).CtrlStyle(co.ES_AUTOHSCROLL|co.ES_NUMBER))
	ok := ui.NewButton(wnd, ui.OptsButton().Text("Go To").Position(ui.Dpi(70, 68)).Width(ui.DpiX(80)).
		CtrlId(uint16(co.ID_OK)).CtrlStyle(co.BS_DEFPUSHBUTTON))
	cancel := ui.NewButton(wnd, ui.OptsButton().Text("Cancel").Position(ui.Dpi(158, 68)).Width(ui.DpiX(80)).
		CtrlId(uint16(co.ID_CANCEL)))
	cancel.On().BnClicked(func() { closeModal(wnd) })
	wnd.On().WmCreate(func(_ ui.WmCreate) int {
		wnd.UiThread(func() {
			edit.Focus()
			edit.SetSelection(0, -1)
		})
		return 0
	})
	target := int64(-1)
	ok.On().BnClicked(func() {
		d := a.ed.Doc()
		n, err := strconv.ParseInt(strings.TrimSpace(edit.Text()), 10, 64)
		if err != nil || n < 1 || n > d.Lines() {
			wnd.Hwnd().MessageBox("The line number is beyond the total number of lines", appName+" - Goto Line", co.MB_OK)
			edit.SetText(strconv.FormatInt(d.Lines(), 10))
			edit.Focus()
			edit.SetSelection(0, -1)
			return
		}
		target = n
		closeModal(wnd)
	})
	wnd.ShowModal()
	if target > 0 {
		a.ed.caretEOL = false
		a.ed.moveTo(a.ed.Doc().LineStart(target-1), false)
	}
}

func (a *App) goToOffset() {
	wnd := newModal(a.wnd, ui.OptsModal().Title("Go To Address").Size(ui.Dpi(250, 104)))
	ui.NewStatic(wnd, ui.OptsStatic().Text("&Address (hex after 0x):").Position(ui.Dpi(12, 12)).Size(ui.Dpi(226, 18)))
	edit := ui.NewEdit(wnd, ui.OptsEdit().Text(fmt.Sprintf("0x%X", a.ed.Caret())).Position(ui.Dpi(12, 34)).
		Width(ui.DpiX(226)).CtrlStyle(co.ES_AUTOHSCROLL))
	ok := ui.NewButton(wnd, ui.OptsButton().Text("Go To").Position(ui.Dpi(70, 68)).Width(ui.DpiX(80)).
		CtrlId(uint16(co.ID_OK)).CtrlStyle(co.BS_DEFPUSHBUTTON))
	cancel := ui.NewButton(wnd, ui.OptsButton().Text("Cancel").Position(ui.Dpi(158, 68)).Width(ui.DpiX(80)).
		CtrlId(uint16(co.ID_CANCEL)))
	cancel.On().BnClicked(func() { closeModal(wnd) })
	wnd.On().WmCreate(func(_ ui.WmCreate) int {
		wnd.UiThread(func() {
			edit.Focus()
			edit.SetSelection(0, -1)
		})
		return 0
	})
	target := int64(-1)
	ok.On().BnClicked(func() {
		size := a.ed.Doc().Len()
		n, err := strconv.ParseInt(strings.TrimSpace(edit.Text()), 0, 64)
		if err != nil || n < 0 || n > size {
			wnd.Hwnd().MessageBox("The address is beyond the end of the file", appName+" - Goto Address", co.MB_OK)
			edit.SetText(fmt.Sprintf("0x%X", size))
			edit.Focus()
			edit.SetSelection(0, -1)
			return
		}
		target = n
		closeModal(wnd)
	})
	wnd.ShowModal()
	if target >= 0 {
		a.ed.moveTo(target, false)
	}
}

func closeModal(wnd *ui.Modal) { wnd.Hwnd().SendMessage(co.WM_CLOSE, 0, 0) }
