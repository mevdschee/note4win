//go:build windows

package ui

// Page Setup and Print. Like Notepad the text is printed in the font of
// the window, wrapped to the page, with a header and a footer that take
// the codes &f (file name), &p (page number), &d (date), &t (time), &l, &c
// and &r (align what follows left, centred or right) and && (an ampersand).

import (
	"syscall"
	"unicode/utf16"
	"unsafe"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"
)

type printState struct {
	devMode, devNames uintptr // the chosen printer and paper, kept while the app runs
}

type pageSetupDlg struct {
	size      uint32
	owner     uintptr
	devMode   uintptr
	devNames  uintptr
	flags     uint32
	paperSize win.POINT
	minMargin win.RECT
	margin    win.RECT
	instance  uintptr
	custData  uintptr
	setupHook uintptr
	paintHook uintptr
	template  *uint16
	hTemplate uintptr
}

type printDlg struct {
	size                                       uint32
	owner                                      uintptr
	devMode                                    uintptr
	devNames                                   uintptr
	hdc                                        uintptr
	flags                                      uint32
	fromPage, toPage, minPage, maxPage, copies uint16
	instance                                   uintptr
	custData                                   uintptr
	printHook, setupHook                       uintptr
	printTemplate, setupTemplate               *uint16
	hPrintTemplate, hSetupTemplate             uintptr
}

const (
	idHeaderEdit = 0x7001
	idFooterEdit = 0x7002
)

var (
	pageSetupApp    *App
	pageSetupHookCb = syscall.NewCallback(pageSetupHook)
)

// pageSetupHook adds the Header and Footer boxes of Notepad to the standard
// Page Setup dialog, below its controls.
func pageSetupHook(hwnd, msg, wParam, lParam uintptr) uintptr {
	a := pageSetupApp
	dlg := win.HWND(hwnd)
	switch co.WM(msg) {
	case co.WM_INITDIALOG:
		font, _ := dlg.SendMessage(co.WM_GETFONT, 0, 0)
		unit := win.RECT{Right: 4, Bottom: 8} // one average character, in dialog units
		dlg.MapDialogRect(&unit)
		ux, uy := unit.Right, unit.Bottom
		grow := uy * 5
		// Make room: a taller dialog, with the buttons moved down.
		wr, _ := dlg.GetWindowRect()
		cr, _ := dlg.GetClientRect()
		var lowest int32
		for _, c := range dlg.EnumChildWindows() {
			r, _ := c.GetWindowRect()
			dlg.ScreenToClientRc(&r)
			if id, _ := c.GetDlgCtrlID(); id == 1 || id == 2 || id == 0x0402 { // OK, Cancel, Printer...
				c.SetWindowPos(0, win.POINT{X: r.Left, Y: r.Top + grow}, win.SIZE{}, co.SWP_NOSIZE|co.SWP_NOZORDER)
			} else if p, _ := c.GetParent(); p == dlg {
				lowest = max(lowest, r.Bottom)
			}
		}
		dlg.SetWindowPos(0, win.POINT{}, win.SIZE{Cx: wr.Right - wr.Left, Cy: wr.Bottom - wr.Top + grow}, co.SWP_NOMOVE|co.SWP_NOZORDER)
		hInst, _ := dlg.HInstance()
		add := func(class, text string, style, exStyle uintptr, x, y, w, h int32, id uintptr) {
			c, _, _ := procCreateWindowExW.Call(exStyle, uintptr(unsafe.Pointer(utf16Ptr(class))),
				uintptr(unsafe.Pointer(utf16Ptr(text))), style|uintptr(co.WS_CHILD|co.WS_VISIBLE),
				uintptr(x), uintptr(y), uintptr(w), uintptr(h), hwnd, id, uintptr(hInst), 0)
			win.HWND(c).SendMessage(co.WM_SETFONT, win.WPARAM(font), 1)
		}
		left, labelW := ux*2, ux*11
		editW := cr.Right - left*2 - labelW
		y := lowest + uy
		add("STATIC", "&Header:", 0, 0, left, y+uy/4, labelW, uy, 0)
		add("EDIT", a.cfg.Header, uintptr(co.WS_TABSTOP)|0x0080, 0x0200, left+labelW, y, editW, uy*3/2, idHeaderEdit)
		y += uy * 2
		add("STATIC", "&Footer:", 0, 0, left, y+uy/4, labelW, uy, 0)
		add("EDIT", a.cfg.Footer, uintptr(co.WS_TABSTOP)|0x0080, 0x0200, left+labelW, y, editW, uy*3/2, idFooterEdit)
	case co.WM_COMMAND:
		if uint16(wParam) == 1 { // OK
			if h, _ := dlg.GetDlgItem(idHeaderEdit); h != 0 {
				a.cfg.Header = windowText(h)
			}
			if h, _ := dlg.GetDlgItem(idFooterEdit); h != 0 {
				a.cfg.Footer = windowText(h)
			}
		}
	}
	return 0
}

func (a *App) pageSetup() {
	psd := pageSetupDlg{
		owner: uintptr(a.wnd.Hwnd()), devMode: a.print.devMode, devNames: a.print.devNames,
		flags:     0x0002 | 0x0004 | 0x2000, // PSD_MARGINS | PSD_INTHOUSANDTHSOFINCHES | PSD_ENABLEPAGESETUPHOOK
		margin:    win.RECT{Left: int32(a.cfg.MarginLeft), Top: int32(a.cfg.MarginTop), Right: int32(a.cfg.MarginRight), Bottom: int32(a.cfg.MarginBottom)},
		setupHook: pageSetupHookCb,
	}
	psd.size = uint32(unsafe.Sizeof(psd))
	pageSetupApp = a
	if r, _, _ := procPageSetupDlgW.Call(uintptr(unsafe.Pointer(&psd))); r == 0 {
		if code, _, _ := procCommDlgExtendedErr.Call(); code != 0 {
			a.wnd.Hwnd().MessageBox("The page setup is not available. Is a printer installed?", appName, co.MB_ICONEXCLAMATION)
		}
		return
	}
	a.print.devMode, a.print.devNames = psd.devMode, psd.devNames
	a.cfg.MarginLeft, a.cfg.MarginTop = int(psd.margin.Left), int(psd.margin.Top)
	a.cfg.MarginRight, a.cfg.MarginBottom = int(psd.margin.Right), int(psd.margin.Bottom)
	a.saveSettings()
}

// printDialog shows the Print dialog and prints.
func (a *App) printDialog() {
	pd := printDlg{
		owner: uintptr(a.wnd.Hwnd()), devMode: a.print.devMode, devNames: a.print.devNames,
		flags:  0x0100 | 0x0004 | 0x0008 | 0x00040000, // PD_RETURNDC | PD_NOSELECTION | PD_NOPAGENUMS | PD_USEDEVMODECOPIESANDCOLLATE
		copies: 1,
	}
	pd.size = uint32(unsafe.Sizeof(pd))
	if r, _, _ := procPrintDlgW.Call(uintptr(unsafe.Pointer(&pd))); r == 0 {
		if code, _, _ := procCommDlgExtendedErr.Call(); code != 0 {
			a.wnd.Hwnd().MessageBox("Cannot print. Is a printer installed?", appName, co.MB_ICONEXCLAMATION)
		}
		return
	}
	a.print.devMode, a.print.devNames = pd.devMode, pd.devNames
	a.printOn(pd.hdc)
}

// printNow prints on the default printer without asking, for "/p file".
func (a *App) printNow() {
	pd := printDlg{owner: uintptr(a.wnd.Hwnd()), flags: 0x0100 | 0x0400} // PD_RETURNDC | PD_RETURNDEFAULT
	pd.size = uint32(unsafe.Sizeof(pd))
	if r, _, _ := procPrintDlgW.Call(uintptr(unsafe.Pointer(&pd))); r == 0 || pd.hdc == 0 {
		a.wnd.Hwnd().MessageBox("Cannot print: there is no default printer.", appName, co.MB_ICONEXCLAMATION)
		return
	}
	a.printOn(pd.hdc)
}

func (a *App) printOn(hdc uintptr) {
	if hdc == 0 {
		return
	}
	restore := waitCursor()
	ok := a.render(hdc)
	restore()
	procDeleteDC.Call(hdc)
	if !ok {
		a.wnd.Hwnd().MessageBox("The "+a.name()+" file could not be printed.", appName, co.MB_ICONEXCLAMATION)
	}
}

const (
	capHorzRes  = 8
	capVertRes  = 10
	capLogPxX   = 88
	capLogPxY   = 90
	capPhysW    = 110
	capPhysH    = 111
	capPhysOffX = 112
	capPhysOffY = 113
)

// render prints the document on a printer device context.
func (a *App) render(hdc uintptr) bool {
	devCap := func(i uintptr) int32 {
		r, _, _ := procGetDeviceCaps.Call(hdc, i)
		return int32(r)
	}
	dpiX, dpiY := devCap(capLogPxX), devCap(capLogPxY)
	inch := func(thousandths int, dpi int32) int32 { return int32(int64(thousandths) * int64(dpi) / 1000) }
	left := max(0, inch(a.cfg.MarginLeft, dpiX)-devCap(capPhysOffX))
	top := max(0, inch(a.cfg.MarginTop, dpiY)-devCap(capPhysOffY))
	right := min(devCap(capHorzRes), devCap(capPhysW)-inch(a.cfg.MarginRight, dpiX)-devCap(capPhysOffX))
	bottom := min(devCap(capVertRes), devCap(capPhysH)-inch(a.cfg.MarginBottom, dpiY)-devCap(capPhysOffY))

	lf := a.ed.lf
	lf.Height = -int32(mulDiv(a.cfg.FontSize, int(dpiY), 720))
	lf.Width = 0
	var font, old uintptr
	if a.binary {
		font, old = fixedFont(hdc, lf)
	} else {
		font = createFont(&lf)
		old, _, _ = procSelectObject.Call(hdc, font)
	}
	defer deleteObject(font)
	defer procSelectObject.Call(hdc, old)
	var tm textMetric
	procGetTextMetricsW.Call(hdc, uintptr(unsafe.Pointer(&tm)))
	lineH := max(1, tm.Height+tm.ExternalLeading)
	bodyTop, bodyBottom := top, bottom
	if a.cfg.Header != "" {
		bodyTop += 2 * lineH
	}
	if a.cfg.Footer != "" {
		bodyBottom -= 2 * lineH
	}
	width := right - left
	if width < tm.AveCharWidth*4 || bodyBottom-bodyTop < lineH {
		return false // the margins leave no room
	}

	di := struct {
		size     int32
		name     *uint16
		output   *uint16
		dataType *uint16
		fwType   uint32
	}{name: utf16Ptr(a.name())}
	di.size = int32(unsafe.Sizeof(di))
	if r, _, _ := procStartDocW.Call(hdc, uintptr(unsafe.Pointer(&di))); int32(r) <= 0 {
		return false
	}
	date, clock := localeFormat(procGetDateFormatW, 0x0001), localeFormat(procGetTimeFormatW, 0x0002)
	out := func(x, y int32, text []uint16) {
		if len(text) > 0 {
			procExtTextOutW.Call(hdc, uintptr(x), uintptr(y), 0, 0, uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)), 0)
		}
	}
	edge := func(format string, y int32, page int) {
		for i, s := range headerParts(format, a.name(), page, date, clock) {
			if s == "" {
				continue
			}
			u := utf16.Encode([]rune(s))
			var sz size
			procGetTextExtentPoint32W.Call(hdc, uintptr(unsafe.Pointer(&u[0])), uintptr(len(u)), uintptr(unsafe.Pointer(&sz)))
			x := left
			switch i {
			case 1:
				x = left + (width-sz.cx)/2
			case 2:
				x = right - sz.cx
			}
			out(x, y, u)
		}
	}

	page, y, open, failed := 0, bodyTop, false, false
	row := func(text []uint16) {
		if failed {
			return
		}
		if !open || y+lineH > bodyBottom {
			if open {
				if r, _, _ := procEndPage.Call(hdc); int32(r) <= 0 {
					failed = true
					return
				}
			}
			if r, _, _ := procStartPage.Call(hdc); int32(r) <= 0 {
				failed = true
				return
			}
			procSelectObject.Call(hdc, font) // a new page resets the device context
			page++
			open, y = true, bodyTop
			edge(a.cfg.Header, top, page)
			edge(a.cfg.Footer, bottom-lineH, page)
		}
		out(left, y, text)
		y += lineH
	}

	d := a.ed.Doc()
	if a.binary { // the rows of the window; what does not fit is cut off
		digits := hexAddrDigits(d.Len())
		var b [hexCols]byte
		var text []uint16
		for off := int64(0); off < d.Len() && !failed; off += hexCols {
			text = hexRow(text[:0], digits, off, b[:d.ReadAt(b[:], off)])
			row(text)
		}
	}
	for off := int64(0); !failed && !a.binary; {
		ls, le, ln := d.LineBounds(off)
		// A long line goes through in parts, each wrapped on its own.
		for part := ls; ; {
			n := min(le-part, segSize)
			chunk := d.Bytes(part, n)
			if part+n < le {
				for cut := 0; cut < 3 && len(chunk) > 1 && chunk[len(chunk)-1]&0xC0 == 0x80; cut++ {
					chunk = chunk[:len(chunk)-1] // not in the middle of a character
				}
				if len(chunk) > 1 && chunk[len(chunk)-1] >= 0xC0 {
					chunk = chunk[:len(chunk)-1]
				}
			}
			part += int64(len(chunk))
			text := expandTabs(utf16.Encode([]rune(string(chunk))))
			for {
				fit := int32(len(text))
				if fit > 0 {
					var sz size
					procGetTextExtentExPointW.Call(hdc, uintptr(unsafe.Pointer(&text[0])), uintptr(len(text)), uintptr(width),
						uintptr(unsafe.Pointer(&fit)), 0, uintptr(unsafe.Pointer(&sz)))
				}
				if int(fit) >= len(text) {
					row(text)
					break
				}
				cut := max(1, int(fit))
				for k := cut; k > 0; k-- { // rather at a space
					if text[k-1] == ' ' {
						cut = k
						break
					}
				}
				row(text[:cut])
				text = text[cut:]
			}
			if part >= le || len(chunk) == 0 {
				break
			}
		}
		if ln == le {
			break
		}
		off = ln
	}
	if failed {
		procAbortDoc.Call(hdc)
		return false
	}
	if open {
		procEndPage.Call(hdc)
	}
	r, _, _ := procEndDoc.Call(hdc)
	return int32(r) > 0
}
