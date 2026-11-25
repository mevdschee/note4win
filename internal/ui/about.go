//go:build windows

package ui

// The About window, laid out like the one of ssh-cloud: the logo on the
// left; on the right the name, the version, a one-line description, the
// author's copyright, the licence and a link to the project. No buttons:
// Esc, Enter or the window's close button close it.

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/mevdschee/note4win/internal/update"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

func inRect(rc win.RECT, pt win.POINT) bool {
	return pt.X >= rc.Left && pt.X < rc.Right && pt.Y >= rc.Top && pt.Y < rc.Bottom
}

func showAbout(a *App) {
	wnd := newModal(a.wnd, ui.OptsModal().Title("About "+appName).Size(ui.Dpi(440, 190)))
	x, w := 124, 300
	text := func(t string, y, h int) *ui.Static {
		return ui.NewStatic(wnd, ui.OptsStatic().Text(t).Position(ui.Dpi(x, y)).Size(ui.Dpi(w, h)).
			CtrlStyle(co.SS_LEFT|co.SS_NOPREFIX))
	}
	ver := "Version " + a.version
	if !update.IsRelease(a.version) {
		ver = "Development build (" + a.version + ")"
	}
	title := text(appName, 20, 34)
	text(ver, 58, 20)
	text("A Notepad for Windows that opens huge files.", 86, 20)
	text(fmt.Sprintf("© %d %s", max(time.Now().Year(), 2026), author), 114, 20)
	text("Released under the MIT License.", 136, 20)

	wnd.On().WmCommandAccelMenu(uint16(co.ID_OK), func() { closeModal(wnd) })     // Enter
	wnd.On().WmCommandAccelMenu(uint16(co.ID_CANCEL), func() { closeModal(wnd) }) // Esc

	msg, _, _ := systemFonts()
	normal := createFont(&msg)
	big := msg
	big.Height = big.Height * 2
	big.Weight = 600
	titleFont := createFont(&big)
	iconSize := ui.DpiX(72)
	hInst, _ := a.wnd.Hwnd().HInstance()
	icon, _, _ := procLoadImageW.Call(uintptr(hInst), iconId, 1, uintptr(iconSize), uintptr(iconSize), 0) // IMAGE_ICON

	wnd.On().WmCreate(func(_ ui.WmCreate) int {
		for _, c := range wnd.Hwnd().EnumChildWindows() {
			c.SendMessage(co.WM_SETFONT, win.WPARAM(normal), 1)
		}
		title.Hwnd().SendMessage(co.WM_SETFONT, win.WPARAM(titleFont), 1)
		return 0
	})
	// The link is painted by the window itself, in the accent colour.
	var linkRect win.RECT
	wnd.On().WmPaint(func() {
		var ps win.PAINTSTRUCT
		hdcW, err := wnd.Hwnd().BeginPaint(&ps)
		if err != nil {
			return
		}
		defer wnd.Hwnd().EndPaint(&ps)
		hdc := uintptr(hdcW)
		if icon != 0 {
			procDrawIconEx.Call(hdc, uintptr(ui.DpiX(26)), uintptr(ui.DpiY(24)), icon, uintptr(iconSize), uintptr(iconSize), 0, 0, 3) // DI_NORMAL
		}
		old, _, _ := procSelectObject.Call(hdc, normal)
		left, top := int32(ui.DpiX(x)), int32(ui.DpiY(158))
		linkRect = win.RECT{Left: left, Top: top, Right: left + textWidth(hdc, repoURL), Bottom: top + int32(ui.DpiY(20))}
		procSetBkMode.Call(hdc, 1)
		procSetTextColor.Call(hdc, uintptr(colors.accent))
		u, _ := syscall.UTF16FromString(repoURL)
		r := linkRect
		procDrawTextW.Call(hdc, uintptr(unsafe.Pointer(&u[0])), ^uintptr(0), uintptr(unsafe.Pointer(&r)), 0x0020|0x0800) // DT_SINGLELINE|DT_NOPREFIX
		procSelectObject.Call(hdc, old)
	})
	wnd.On().WmSetCursor(func(p ui.WmSetCursor) bool {
		if p.Hwnd() != wnd.Hwnd() || p.Raw.LParam.LoWord() != 1 { // not our client area
			return wnd.Hwnd().DefWindowProc(co.WM_SETCURSOR, p.Raw.WParam, p.Raw.LParam) != 0
		}
		pt, err := win.GetCursorPos()
		if err == nil {
			wnd.Hwnd().ScreenToClientPt(&pt)
		}
		if err == nil && inRect(linkRect, pt) {
			procSetCursor.Call(loadCursor(32649)) // IDC_HAND
		} else {
			procSetCursor.Call(loadCursor(idcArrow))
		}
		return true
	})
	wnd.On().WmLButtonUp(func(p ui.WmMouse) {
		if inRect(linkRect, p.Pos()) {
			shellOpen(wnd.Hwnd(), repoURL)
		}
	})
	wnd.ShowModal()

	if icon != 0 {
		procDestroyIcon.Call(icon)
	}
	deleteObject(normal)
	deleteObject(titleFont)
}

// installed reports whether this is the copy the MSI installed, in
// %LOCALAPPDATA%\Programs\note4win, and not the portable exe.
func installed() bool {
	exe, err := os.Executable()
	if err != nil {
		return false
	}
	dir := filepath.Join(os.Getenv("LOCALAPPDATA"), "Programs", "note4win")
	return strings.EqualFold(filepath.Clean(filepath.Dir(exe)), filepath.Clean(dir))
}

// checkForUpdates runs from the Help menu: it asks GitHub for the latest
// release and offers to install it when it is newer. Nothing is checked
// automatically.
func (a *App) checkForUpdates() {
	h := a.wnd.Hwnd()
	if !update.IsRelease(a.version) {
		h.MessageBox("This is a development build ("+a.version+"). Updates are only for installed releases.", appName, co.MB_ICONINFORMATION)
		return
	}
	restore := waitCursor()
	r, ok, err := update.Latest(context.Background(), a.version)
	restore()
	switch {
	case err != nil:
		h.MessageBox("Could not check for updates.\n\n"+err.Error(), appName, co.MB_ICONEXCLAMATION)
		return
	case !ok || !update.Newer(r.Version, a.version):
		h.MessageBox(appName+" is up to date. You have the latest version ("+a.version+").", appName, co.MB_ICONINFORMATION)
		return
	case !installed():
		// A portable copy: the MSI would install a second copy next to it.
		ret, _ := h.MessageBox(fmt.Sprintf("%s %s is available (you have %s).\n\nThis copy is not installed, so it cannot update itself. Open the download page?",
			appName, r.Version, a.version), appName, co.MB_YESNO|co.MB_ICONINFORMATION)
		if ret == co.ID_YES {
			shellOpen(h, repoURL+"/releases/latest")
		}
		return
	}
	ret, _ := h.MessageBox(fmt.Sprintf("%s %s is available (you have %s).\n\nDownload and install it now? %s closes during the installation.",
		appName, r.Version, a.version, appName), appName, co.MB_YESNO|co.MB_ICONINFORMATION)
	if ret != co.ID_YES || !a.confirmSave() {
		return
	}
	restore = waitCursor()
	path, err := update.Download(context.Background(), r, os.TempDir(), nil)
	restore()
	if err == nil {
		// msiexec shows the installer; the MSI replaces the running version,
		// so close to release note4win.exe.
		log := filepath.Join(os.TempDir(), "note4win-update.log")
		err = exec.Command("msiexec", "/i", path, "/l*v", log).Start()
	}
	if err != nil {
		h.MessageBox("Could not install the update.\n\n"+err.Error(), appName, co.MB_ICONEXCLAMATION)
		return
	}
	a.ed.Doc().SetSaved() // the question was asked above
	h.PostMessage(co.WM_CLOSE, 0, 0)
}
