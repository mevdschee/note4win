//go:build windows

package ui

// Light and dark mode, as in ssh-cloud. The window follows the "app mode" of
// Windows (Settings > Personalization > Colors), also when it changes while
// the app runs. Popup menus and scrollbars only turn dark through
// undocumented uxtheme functions, which exist on Windows 10 1809 and later;
// the menu bar and the status bar have no dark look at all in Windows and
// are painted here in dark mode. In light mode Windows draws them, as it does
// for Notepad.
//
// TEXT4WIN_THEME=dark (or light) overrides the system setting, which helps
// to try the dark look under Wine.

import (
	"os"
	"syscall"
	"unsafe"

	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
	"golang.org/x/sys/windows"
	"golang.org/x/sys/windows/registry"
)

// palette holds the colours of one mode (COLORREF, 0x00BBGGRR).
type palette struct {
	window       uint32 // the text area
	text         uint32
	textDisabled uint32
	selection    uint32
	selectedText uint32
	dialog       uint32 // dialog background
	field        uint32 // edit boxes in dialogs
	accent       uint32 // links
	bar          uint32 // menu bar and status bar
	barHot       uint32 // menu bar item under the mouse
	barLine      uint32 // separators in the bars
}

var (
	lightPalette = palette{accent: 0x00C06700, barHot: 0x00FFF3E5} // #0067C0, #E5F3FF
	darkPalette  = palette{
		window:       0x00272727,
		text:         0x00FFFFFF,
		textDisabled: 0x006D6D6D,
		selection:    0x00D77800, // #0078D7
		selectedText: 0x00FFFFFF,
		dialog:       0x00202020,
		field:        0x002D2D2D,
		accent:       0x00FFCD60, // #60CDFF
		bar:          0x00202020,
		barHot:       0x003D3D3D,
		barLine:      0x003A3A3A,
	}
	darkMode bool
	colors   palette
	// ownBar tells that the menu bar is painted here, which is in dark mode.
	ownBar bool
)

// Undocumented uxtheme exports, by ordinal (0 when missing).
var (
	uxAllowDarkModeForWindow uintptr // 133
	uxSetPreferredAppMode    uintptr // 135 (AllowDarkModeForApp on 1809)
	uxFlushMenuThemes        uintptr // 136
	uxRefreshImmersivePolicy uintptr // 104
)

func initTheme() {
	if !isWine() { // Wine has no such exports, and might have others there
		if _, _, build := windows.RtlGetNtVersionNumbers(); build&0xffff >= 17763 {
			if h, err := windows.LoadLibraryEx("uxtheme.dll", 0, windows.LOAD_LIBRARY_SEARCH_SYSTEM32); err == nil {
				get := func(ord uintptr) uintptr {
					p, _ := windows.GetProcAddressByOrdinal(h, ord)
					return p
				}
				uxAllowDarkModeForWindow = get(133)
				uxSetPreferredAppMode = get(135)
				uxFlushMenuThemes = get(136)
				uxRefreshImmersivePolicy = get(104)
				if uxSetPreferredAppMode != 0 {
					syscall.SyscallN(uxSetPreferredAppMode, 1) // AllowDark: follow the system
				}
			}
		}
	}
	darkMode = !systemDark() // so that loadTheme sees a change
	loadTheme()
}

// systemDark reports whether Windows apps should be dark. With a High
// Contrast theme the app stays light, which uses the system colours.
func systemDark() bool {
	switch os.Getenv("TEXT4WIN_THEME") {
	case "dark":
		return true
	case "light":
		return false
	}
	if highContrast() {
		return false
	}
	k, err := registry.OpenKey(registry.CURRENT_USER,
		`Software\Microsoft\Windows\CurrentVersion\Themes\Personalize`, registry.QUERY_VALUE)
	if err != nil {
		return false
	}
	defer k.Close()
	v, _, err := k.GetIntegerValue("AppsUseLightTheme")
	return err == nil && v == 0
}

func highContrast() bool {
	type highContrast struct {
		size, flags uint32
		scheme      uintptr
	}
	hc := highContrast{size: uint32(unsafe.Sizeof(highContrast{}))}
	r, _, _ := procSystemParametersInfoW.Call(spiGetHighContrast, uintptr(hc.size), uintptr(unsafe.Pointer(&hc)), 0)
	return r != 0 && hc.flags&1 != 0
}

// loadTheme reads the system mode into darkMode and colors; it reports
// whether the mode changed.
func loadTheme() bool {
	if uxRefreshImmersivePolicy != 0 {
		syscall.SyscallN(uxRefreshImmersivePolicy)
	}
	dark := systemDark()
	changed := dark != darkMode
	darkMode = dark
	if dark {
		colors = darkPalette
	} else {
		colors = lightPalette
		colors.window = sysColor(co.COLOR_WINDOW)
		colors.text = sysColor(co.COLOR_WINDOWTEXT)
		colors.textDisabled = sysColor(co.COLOR_GRAYTEXT)
		colors.selection = sysColor(co.COLOR_HIGHLIGHT)
		colors.selectedText = sysColor(co.COLOR_HIGHLIGHTTEXT)
		colors.dialog = sysColor(co.COLOR_BTNFACE)
		colors.field = sysColor(co.COLOR_WINDOW)
		colors.bar = sysColor(co.COLOR_BTNFACE) // what the status bar has
	}
	ownBar = dark
	if changed && uxFlushMenuThemes != 0 {
		syscall.SyscallN(uxFlushMenuThemes)
	}
	return changed
}

func setTheme(h win.HWND, name string) {
	var p *uint16
	if name != "" {
		p = utf16Ptr(name)
	}
	procSetWindowTheme.Call(uintptr(h), uintptr(unsafe.Pointer(p)), 0)
}

// allowDark lets a window use the dark variants of its theme (and of its
// menus and scrollbars), where Windows has the export for it.
func allowDark(h win.HWND) {
	if uxAllowDarkModeForWindow != 0 {
		syscall.SyscallN(uxAllowDarkModeForWindow, uintptr(h), boolArg(darkMode))
	}
}

// themeFrame makes a window's title bar dark or light.
func themeFrame(h win.HWND) {
	v := uint32(boolArg(darkMode))
	allowDark(h)
	if r, _, _ := procDwmSetWindowAttr.Call(uintptr(h), 20, uintptr(unsafe.Pointer(&v)), 4); r != 0 { // DWMWA_USE_IMMERSIVE_DARK_MODE
		procDwmSetWindowAttr.Call(uintptr(h), 19, uintptr(unsafe.Pointer(&v)), 4) // before Windows 10 20H1
	}
}

// themeControl gives a control in a dialog the look of the current mode.
func themeControl(h win.HWND) {
	cls, _ := h.GetClassName()
	name := ""
	switch cls {
	case "Button", "ScrollBar":
		name = "DarkMode_Explorer"
	case "Edit", "ComboBox":
		name = "DarkMode_CFD"
	default:
		return
	}
	if !darkMode {
		name = "Explorer"
	}
	allowDark(h)
	setTheme(h, name)
	h.SendMessage(co.WM_THEMECHANGED, 0, 0)
}

// themeDialog themes a window and its controls.
func themeDialog(h win.HWND) {
	themeFrame(h)
	if darkMode || uxAllowDarkModeForWindow != 0 {
		for _, c := range h.EnumChildWindows() {
			themeControl(c)
		}
	}
	procRedrawWindow.Call(uintptr(h), 0, 0, 0x0001|0x0004|0x0080|0x0400) // RDW_INVALIDATE|RDW_ERASE|RDW_ALLCHILDREN|RDW_FRAME
}

// applyTheme gives the main window the colours of the current mode.
func (a *App) applyTheme() {
	h := a.wnd.Hwnd()
	themeFrame(h)
	a.ed.SetColors(editorColors{bg: colors.window, fg: colors.text, selBg: colors.selection, selFg: colors.selectedText})
	allowDark(a.ed.Hwnd())
	if darkMode {
		setTheme(a.ed.Hwnd(), "DarkMode_Explorer")
	} else {
		setTheme(a.ed.Hwnd(), "Explorer")
	}
	if !a.sbHooked {
		a.sbHooked = true
		sbApp = a
		procSetWindowSubclass.Call(uintptr(a.sb.Hwnd()), sbSubclassCb, 0x5442, 0)
	}
	if a.find.dlg != 0 {
		themeDialog(a.find.dlg)
	}
	// The background of the menu bar, for where the paint messages below do
	// not exist (Wine).
	mi := struct {
		size, mask, style, cyMax uint32
		back                     uintptr
		helpID                   uint32
		data                     uintptr
	}{mask: 0x0002} // MIM_BACKGROUND
	mi.size = uint32(unsafe.Sizeof(mi))
	if ownBar {
		mi.back = solidBrush(colors.bar)
	}
	procSetMenuInfo.Call(uintptr(a.menu), uintptr(unsafe.Pointer(&mi)))
	h.DrawMenuBar()
	h.SetWindowPos(0, win.POINT{}, win.SIZE{}, co.SWP_NOMOVE|co.SWP_NOSIZE|co.SWP_NOZORDER|co.SWP_FRAMECHANGED)
	procRedrawWindow.Call(uintptr(h), 0, 0, 0x0001|0x0004|0x0080|0x0400)
}

// newModal is ui.NewModal in the current mode: dialog background, label
// and edit colours, and the dark control themes.
func newModal(parent ui.Parent, opts *ui.VarOptsModal) *ui.Modal {
	pal := colors
	opts.ClassBrush(win.HBRUSH(solidBrush(pal.dialog)))
	wnd := ui.NewModal(parent, opts)
	on := wnd.On()
	on.WmCtlColorStatic(func(p ui.WmCtlColor) win.HBRUSH {
		procSetTextColor.Call(uintptr(p.Hdc()), uintptr(pal.text))
		procSetBkColor.Call(uintptr(p.Hdc()), uintptr(pal.dialog))
		return win.HBRUSH(solidBrush(pal.dialog))
	})
	if darkMode {
		on.WmCtlColorEdit(func(p ui.WmCtlColor) win.HBRUSH {
			procSetTextColor.Call(uintptr(p.Hdc()), uintptr(pal.text))
			procSetBkColor.Call(uintptr(p.Hdc()), uintptr(pal.field))
			return win.HBRUSH(solidBrush(pal.field))
		})
		on.WmCtlColorBtn(func(p ui.WmCtlColor) win.HBRUSH {
			procSetBkColor.Call(uintptr(p.Hdc()), uintptr(pal.dialog))
			return win.HBRUSH(solidBrush(pal.dialog))
		})
	}
	on.Wm(co.WM_SHOWWINDOW, func(p ui.Wm) uintptr {
		if p.WParam != 0 {
			themeDialog(wnd.Hwnd())
		}
		return wnd.Hwnd().DefWindowProc(p.Msg, p.WParam, p.LParam)
	})
	return wnd
}

// ---- the menu bar ----
//
// Windows draws the menu bar white, with two thin lines under it: the lower
// edge of the bar and a line in the colour of the status bar, between the
// bar and the text. That is what Notepad looks like, so in light mode
// nothing is done here. There is no dark menu bar in Windows: in dark mode
// it is painted here, in the colour of the status bar, through the
// undocumented messages that Windows sends to paint it.

const (
	wmUahDrawMenu     = co.WM(0x0091)
	wmUahDrawMenuItem = co.WM(0x0092)
	objidMenu         = ^uintptr(2) // -3
)

type menuBarInfo struct {
	size    uint32
	bar     win.RECT
	menu    uintptr
	wnd     uintptr
	focused int32
}

// uahMenu and uahDrawMenuItem are the undocumented structures of the menu
// bar's paint messages (64 bit layout).
type uahMenu struct {
	menu  uintptr
	hdc   uintptr
	flags uint32
}

type uahDrawMenuItem struct {
	ctlType, ctlID, itemID, itemAction, itemState uint32
	hwndItem                                      uintptr
	hdc                                           uintptr
	rcItem                                        win.RECT
	itemData                                      uintptr
	um                                            uahMenu
	position                                      int32
}

func (a *App) menuBarEvents() {
	on := a.wnd.On()
	on.Wm(wmUahDrawMenu, func(p ui.Wm) uintptr {
		h := a.wnd.Hwnd()
		if !ownBar {
			return h.DefWindowProc(p.Msg, p.WParam, p.LParam)
		}
		um := *(**uahMenu)(unsafe.Pointer(&p.LParam))
		mbi := menuBarInfo{size: uint32(unsafe.Sizeof(menuBarInfo{}))}
		procGetMenuBarInfo.Call(uintptr(h), objidMenu, 0, uintptr(unsafe.Pointer(&mbi)))
		wr, _ := h.GetWindowRect()
		rc := mbi.bar
		win.OffsetRect(&rc, int(-wr.Left), int(-wr.Top))
		fillRect(um.hdc, &rc, colors.bar)
		return 1
	})
	on.Wm(wmUahDrawMenuItem, func(p ui.Wm) uintptr {
		h := a.wnd.Hwnd()
		if !ownBar {
			return h.DefWindowProc(p.Msg, p.WParam, p.LParam)
		}
		d := *(**uahDrawMenuItem)(unsafe.Pointer(&p.LParam))
		var text [128]uint16
		mii := struct {
			size, mask, fType, fState, id uint32
			subMenu, checked, unchecked   uintptr
			itemData                      uintptr
			typeData                      *uint16
			cch                           uint32
			bmpItem                       uintptr
		}{mask: 0x0040, typeData: &text[0], cch: uint32(len(text) - 1)} // MIIM_STRING
		mii.size = uint32(unsafe.Sizeof(mii))
		procGetMenuItemInfoW.Call(d.um.menu, uintptr(d.position), 1, uintptr(unsafe.Pointer(&mii)))
		bg, fg := colors.bar, colors.text
		if d.itemState&(0x0040|0x0001) != 0 { // ODS_HOTLIGHT, ODS_SELECTED
			bg = colors.barHot
		}
		if d.itemState&(0x0002|0x0004) != 0 { // ODS_GRAYED, ODS_DISABLED
			fg = colors.textDisabled
		}
		flags := uintptr(0x0001 | 0x0004 | 0x0020) // DT_CENTER|DT_VCENTER|DT_SINGLELINE
		if d.itemState&0x0100 != 0 {               // ODS_NOACCEL
			flags |= 0x00100000 // DT_HIDEPREFIX
		}
		fillRect(d.um.hdc, &d.rcItem, bg)
		procSetBkMode.Call(d.um.hdc, 1)
		procSetTextColor.Call(d.um.hdc, uintptr(fg))
		procDrawTextW.Call(d.um.hdc, uintptr(unsafe.Pointer(&text[0])), ^uintptr(0), uintptr(unsafe.Pointer(&d.rcItem)), flags)
		return 1
	})
	// The line between the menu bar and the text is not part of the bar
	// and stays light in dark mode; paint a dark one over it.
	for _, msg := range []co.WM{co.WM_NCPAINT, co.WM_NCACTIVATE} {
		on.Wm(msg, func(p ui.Wm) uintptr {
			h := a.wnd.Hwnd()
			r := h.DefWindowProc(p.Msg, p.WParam, p.LParam)
			if ownBar && !isWine() {
				rc, _ := h.GetClientRect()
				h.ClientToScreenRc(&rc)
				wr, _ := h.GetWindowRect()
				line := win.RECT{Left: rc.Left - wr.Left, Right: rc.Right - wr.Left, Top: rc.Top - wr.Top - 1, Bottom: rc.Top - wr.Top}
				if hdc, err := h.GetWindowDC(); err == nil {
					fillRect(uintptr(hdc), &line, colors.barLine)
					h.ReleaseDC(hdc)
				}
			}
			return r
		})
	}
}

// ---- the status bar in dark mode ----

var (
	sbApp        *App
	sbSubclassCb = syscall.NewCallback(sbSubclassProc)
)

func sbSubclassProc(hwnd, msg, wParam, lParam, id, ref uintptr) uintptr {
	if darkMode && sbApp != nil {
		switch co.WM(msg) {
		case co.WM_ERASEBKGND:
			return 1
		case co.WM_PAINT:
			sbApp.paintStatusBar(win.HWND(hwnd))
			return 0
		}
	}
	r, _, _ := procDefSubclassProc.Call(hwnd, msg, wParam, lParam)
	return r
}

func (a *App) paintStatusBar(h win.HWND) {
	var ps win.PAINTSTRUCT
	hdcW, err := h.BeginPaint(&ps)
	if err != nil {
		return
	}
	defer h.EndPaint(&ps)
	hdc := uintptr(hdcW)
	rc, _ := h.GetClientRect()
	fillRect(hdc, &rc, colors.bar)
	font, _ := h.SendMessage(co.WM_GETFONT, 0, 0)
	old, _, _ := procSelectObject.Call(hdc, font)
	procSetBkMode.Call(hdc, 1)
	procSetTextColor.Call(hdc, uintptr(colors.text))
	for i, text := range a.status {
		var part win.RECT
		h.SendMessage(co.SB_GETRECT, win.WPARAM(i+1), win.LPARAM(unsafe.Pointer(&part)))
		line := win.RECT{Left: part.Left - 1, Right: part.Left, Top: part.Top + 2, Bottom: part.Bottom - 2}
		fillRect(hdc, &line, colors.barLine)
		u, _ := syscall.UTF16FromString(text)
		part.Left += 2
		procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&u[0])), ^uintptr(0), uintptr(unsafe.Pointer(&part)), 0x0004|0x0020|0x0800) // DT_VCENTER|DT_SINGLELINE|DT_NOPREFIX
	}
	procSelectObject.Call(hdc, old)
}
