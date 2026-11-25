//go:build windows

package ui

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"unsafe"

	"github.com/mevdschee/note4win/internal/config"
	"github.com/mevdschee/note4win/internal/doc"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
	"golang.org/x/sys/windows"
)

const (
	appName  = "note4win"
	repoURL  = "https://github.com/mevdschee/note4win"
	author   = "Maurits van der Schee"
	untitled = "Untitled"
	iconId   = 1 // RT_GROUP_ICON "#1" in winres/winres.json

	timerProgress = 1
)

// Menu and accelerator commands. They are sent on to the main window when
// a key is pressed in the Find dialog, so they stay clear of control IDs.
const (
	cmdNew uint16 = 0x9000 + iota
	cmdNewWindow
	cmdOpen
	cmdSave
	cmdSaveAs
	cmdPageSetup
	cmdPrint
	cmdExit
	cmdUndo
	cmdCut
	cmdCopy
	cmdPaste
	cmdDelete
	cmdFind
	cmdFindNext
	cmdFindPrev
	cmdReplace
	cmdGoTo
	cmdSelectAll
	cmdTimeDate
	cmdWordWrap
	cmdFont
	cmdZoomIn
	cmdZoomOut
	cmdZoomReset
	cmdStatusBar
	cmdHelp
	cmdUpdate
	cmdAbout
	cmdTextMode
	cmdBinaryMode
	cmdToggleMode
	cmdLast
)

// App is the main window with its document.
type App struct {
	wnd     *ui.Main
	ed      *Editor
	sb      *ui.StatusBar
	menu    win.HMENU
	cfg     *config.Settings
	cfgDir  string
	version string

	path string // empty for a new document
	enc  doc.Encoding
	// binary is the binary mode: the document is the bytes of the file as
	// they are, also when enc says that the text mode would convert them.
	binary bool
	file   *doc.File // the opened file, while the document uses its memory

	find   findState
	busy   bool // a file is loading
	stop   *atomic.Bool
	loaded atomic.Int64 // bytes, for the progress in the status bar
	total  int64

	title  string
	status [4]string
	print  printState

	sbHooked bool
}

// Options are the command line arguments.
type Options struct {
	Path   string // file to open
	Print  bool   // print it and exit
	Binary bool   // start in binary mode
}

// Run shows the main window and returns the exit code.
func Run(version string, opts Options) int {
	a := &App{version: version, enc: doc.UTF8, stop: new(atomic.Bool)}
	a.cfgDir, _ = config.Dir()
	a.cfg = config.Load(a.cfgDir)
	a.find.matchCase, a.find.wrapAround, a.find.down = a.cfg.MatchCase, a.cfg.WrapAround, true
	initTheme()

	a.menu = buildMenu()
	a.wnd = ui.NewMain(ui.OptsMain().
		Title(untitled + " - " + appName).
		ClassIconId(iconId).
		ClassBrush(win.HBRUSH(0)).
		Menu(a.menu).
		AccelTable(accelerators()).
		Size(ui.Dpi(900, 540)).
		Center(true).
		CmdShow(a.showCommand()).
		Style(co.WS_OVERLAPPEDWINDOW | co.WS_CLIPCHILDREN))

	a.ed = NewEditor(a.wnd)
	a.ed.wrap = a.cfg.WordWrap
	a.ed.lf = a.fontFromSettings()
	a.ed.OnChange = a.update
	a.ed.OnContextMenu = a.contextMenu
	a.ed.OnZoom = func(dir int) { a.zoom(a.ed.Zoom() + 10*dir) }

	a.sb = ui.NewStatusBar(a.wnd, ui.OptsStatusBar().
		FlexPart(1, "").
		FixedPart(ui.DpiX(140), "").
		FixedPart(ui.DpiX(50), "").
		FixedPart(ui.DpiX(120), "").
		FixedPart(ui.DpiX(120), ""))

	a.events(opts)
	return a.wnd.RunAsMain()
}

func (a *App) fontFromSettings() logFont {
	var lf logFont
	lf.Height = -int32(mulDiv(a.cfg.FontSize, ui.DpiY(96), 720))
	lf.Weight = int32(a.cfg.FontWeight)
	if a.cfg.FontItalic {
		lf.Italic = 1
	}
	lf.CharSet = byte(a.cfg.FontCharSet)
	lf.setFace(a.cfg.FontFace)
	return lf
}

func buildMenu() win.HMENU {
	bar, _ := win.CreateMenu()
	popup := func(title string, items ...any) win.HMENU {
		m, _ := win.CreatePopupMenu()
		for i := 0; i < len(items); i++ {
			switch v := items[i].(type) {
			case nil:
				appendMenu(m, mfSeparator, 0, "")
			case uint16:
				i++
				appendMenu(m, mfString, uintptr(v), items[i].(string))
			case win.HMENU:
				i++
				appendMenu(m, mfPopup, uintptr(v), items[i].(string))
			}
		}
		if title != "" {
			appendMenu(bar, mfPopup, uintptr(m), title)
		}
		return m
	}
	popup("&File",
		cmdNew, "&New\tCtrl+N",
		cmdNewWindow, "New &Window\tCtrl+Shift+N",
		cmdOpen, "&Open...\tCtrl+O",
		cmdSave, "&Save\tCtrl+S",
		cmdSaveAs, "Save &As...\tCtrl+Shift+S",
		nil,
		cmdPageSetup, "Page Set&up...",
		cmdPrint, "&Print...\tCtrl+P",
		nil,
		cmdExit, "E&xit")
	popup("&Edit",
		cmdUndo, "&Undo\tCtrl+Z",
		nil,
		cmdCut, "Cu&t\tCtrl+X",
		cmdCopy, "&Copy\tCtrl+C",
		cmdPaste, "&Paste\tCtrl+V",
		cmdDelete, "De&lete\tDel",
		nil,
		cmdFind, "&Find...\tCtrl+F",
		cmdFindNext, "Find &Next\tF3",
		cmdFindPrev, "Find Pre&vious\tShift+F3",
		cmdReplace, "&Replace...\tCtrl+H",
		cmdGoTo, "&Go To...\tCtrl+G",
		nil,
		cmdSelectAll, "Select &All\tCtrl+A",
		cmdTimeDate, "Time/&Date\tF5")
	popup("F&ormat",
		cmdWordWrap, "&Word Wrap",
		cmdFont, "&Font...")
	zoom := popup("",
		cmdZoomIn, "Zoom &In\tCtrl+Plus",
		cmdZoomOut, "Zoom &Out\tCtrl+Minus",
		cmdZoomReset, "&Restore Default Zoom\tCtrl+0")
	popup("&View",
		cmdTextMode, "&Text Mode\tCtrl+B",
		cmdBinaryMode, "&Binary Mode\tCtrl+B",
		nil,
		zoom, "&Zoom",
		cmdStatusBar, "&Status Bar")
	popup("&Help",
		cmdHelp, "View &Help",
		cmdUpdate, "Check for &Updates...",
		nil,
		cmdAbout, "&About "+appName)
	return bar
}

// accelerators are the keys that work in the whole window. The keys of the
// clipboard and undo are not here: the editor handles them itself, so that
// they keep working in the edit boxes of the Find dialog.
func accelerators() win.HACCEL {
	const ctrl = co.ACCELF_VIRTKEY | co.ACCELF_CONTROL
	const ctrlShift = ctrl | co.ACCELF_SHIFT
	h, _ := win.CreateAcceleratorTable(
		win.ACCEL{FVirt: ctrl, Key: co.VK('N'), Cmd: cmdNew},
		win.ACCEL{FVirt: ctrlShift, Key: co.VK('N'), Cmd: cmdNewWindow},
		win.ACCEL{FVirt: ctrl, Key: co.VK('O'), Cmd: cmdOpen},
		win.ACCEL{FVirt: ctrl, Key: co.VK('S'), Cmd: cmdSave},
		win.ACCEL{FVirt: ctrlShift, Key: co.VK('S'), Cmd: cmdSaveAs},
		win.ACCEL{FVirt: ctrl, Key: co.VK('P'), Cmd: cmdPrint},
		win.ACCEL{FVirt: ctrl, Key: co.VK('W'), Cmd: cmdExit},
		win.ACCEL{FVirt: ctrl, Key: co.VK('F'), Cmd: cmdFind},
		win.ACCEL{FVirt: co.ACCELF_VIRTKEY, Key: co.VK_F3, Cmd: cmdFindNext},
		win.ACCEL{FVirt: co.ACCELF_VIRTKEY | co.ACCELF_SHIFT, Key: co.VK_F3, Cmd: cmdFindPrev},
		win.ACCEL{FVirt: ctrl, Key: co.VK('H'), Cmd: cmdReplace},
		win.ACCEL{FVirt: ctrl, Key: co.VK('G'), Cmd: cmdGoTo},
		win.ACCEL{FVirt: ctrl, Key: co.VK('B'), Cmd: cmdToggleMode},
		win.ACCEL{FVirt: co.ACCELF_VIRTKEY, Key: co.VK_F5, Cmd: cmdTimeDate},
		win.ACCEL{FVirt: ctrl, Key: co.VK_OEM_PLUS, Cmd: cmdZoomIn},
		win.ACCEL{FVirt: ctrlShift, Key: co.VK_OEM_PLUS, Cmd: cmdZoomIn},
		win.ACCEL{FVirt: ctrl, Key: co.VK_ADD, Cmd: cmdZoomIn},
		win.ACCEL{FVirt: ctrl, Key: co.VK_OEM_MINUS, Cmd: cmdZoomOut},
		win.ACCEL{FVirt: ctrl, Key: co.VK_SUBTRACT, Cmd: cmdZoomOut},
		win.ACCEL{FVirt: ctrl, Key: co.VK('0'), Cmd: cmdZoomReset},
		win.ACCEL{FVirt: ctrl, Key: co.VK_NUMPAD0, Cmd: cmdZoomReset},
		win.ACCEL{FVirt: co.ACCELF_VIRTKEY, Key: co.VK_F1, Cmd: cmdHelp},
	)
	return h
}

func (a *App) events(opts Options) {
	on := a.wnd.On()
	on.WmCreate(func(_ ui.WmCreate) int {
		a.restoreWindow()
		procDragAcceptFiles.Call(uintptr(a.wnd.Hwnd()), 1)
		a.applyTheme()
		if !a.cfg.StatusBar {
			a.sb.Hwnd().ShowWindow(co.SW_HIDE)
		}
		a.layout()
		a.update()
		// Open the file once the window is up, so that messages have an owner.
		a.wnd.Hwnd().PostMessage(wmStart, 0, 0)
		return 0
	})
	on.Wm(wmStart, func(_ ui.Wm) uintptr {
		a.ed.Hwnd().SetFocus()
		a.start(opts)
		return 0
	})
	on.WmSize(func(p ui.WmSize) {
		if p.Request() != co.SIZE_REQ_MINIMIZED {
			a.layout()
		}
	})
	on.WmEraseBkgnd(func(_ ui.WmEraseBkgnd) int { return 1 })
	on.WmPaint(func() { // the line between the menu bar and the text
		var ps win.PAINTSTRUCT
		hdc, err := a.wnd.Hwnd().BeginPaint(&ps)
		if err != nil {
			return
		}
		rc, _ := a.wnd.Hwnd().GetClientRect()
		rc.Bottom = a.menuLine()
		fillRect(uintptr(hdc), &rc, colors.barLine)
		a.wnd.Hwnd().EndPaint(&ps)
	})
	on.WmSetFocus(func(_ ui.WmSetFocus) { a.ed.Hwnd().SetFocus() })
	on.WmClose(func() {
		if a.busy {
			a.stop.Store(true) // the loader closes the window when it notices
			return
		}
		if !a.confirmSave() {
			return
		}
		a.saveSettings()
		a.closeFind()
		a.wnd.Hwnd().DestroyWindow()
	})
	on.WmDestroy(func() {
		if a.file != nil {
			a.file.Close()
		}
	})
	on.Wm(co.WM_QUERYENDSESSION, func(_ ui.Wm) uintptr {
		if a.busy || a.confirmSave() {
			a.saveSettings()
			return 1
		}
		return 0
	})
	on.WmInitMenuPopup(func(_ ui.WmInitMenuPopup) { a.updateMenu(a.menu) })
	on.WmDropFiles(func(p ui.WmDropFiles) {
		files, err := p.HDrop().DragQueryFile()
		p.HDrop().DragFinish()
		if err != nil || len(files) == 0 || a.busy {
			return
		}
		procSetForegroundWindow.Call(uintptr(a.wnd.Hwnd()))
		if a.confirmSave() {
			a.open(files[0])
		}
	})
	on.WmTimer(timerProgress, a.showProgress)
	on.WmGetMinMaxInfo(func(p ui.WmGetMinMaxInfo) {
		p.Info().PtMinTrackSize = win.POINT{X: int32(ui.DpiX(200)), Y: int32(ui.DpiY(120))}
	})
	for _, msg := range []co.WM{wmSettingChange, co.WM_SYSCOLORCHANGE, co.WM_THEMECHANGED} {
		on.Wm(msg, func(p ui.Wm) uintptr {
			if loadTheme() || p.Msg != wmSettingChange {
				a.applyTheme()
			}
			return a.wnd.Hwnd().DefWindowProc(p.Msg, p.WParam, p.LParam)
		})
	}
	a.menuBarEvents()

	commands := map[uint16]func(){
		cmdNew:        a.newFile,
		cmdNewWindow:  a.newWindow,
		cmdOpen:       a.openDialog,
		cmdSave:       func() { a.save() },
		cmdSaveAs:     func() { a.saveAs() },
		cmdPageSetup:  a.pageSetup,
		cmdPrint:      func() { a.printDialog() },
		cmdExit:       func() { a.wnd.Hwnd().PostMessage(co.WM_CLOSE, 0, 0) },
		cmdUndo:       a.ed.Undo,
		cmdCut:        a.ed.Cut,
		cmdCopy:       func() { a.ed.Copy() },
		cmdPaste:      a.ed.Paste,
		cmdDelete:     a.ed.Delete,
		cmdFind:       func() { a.showFind(false) },
		cmdFindNext:   func() { a.findAgain(true) },
		cmdFindPrev:   func() { a.findAgain(false) },
		cmdReplace:    func() { a.showFind(true) },
		cmdGoTo:       a.goTo,
		cmdSelectAll:  a.ed.SelectAll,
		cmdTimeDate:   a.timeDate,
		cmdWordWrap:   a.toggleWrap,
		cmdFont:       a.chooseFont,
		cmdZoomIn:     func() { a.zoom(a.ed.Zoom() + 10) },
		cmdZoomOut:    func() { a.zoom(a.ed.Zoom() - 10) },
		cmdZoomReset:  func() { a.zoom(100) },
		cmdStatusBar:  a.toggleStatusBar,
		cmdHelp:       func() { shellOpen(a.wnd.Hwnd(), repoURL+"#readme") },
		cmdUpdate:     a.checkForUpdates,
		cmdAbout:      func() { showAbout(a) },
		cmdTextMode:   func() { a.setBinary(false) },
		cmdBinaryMode: func() { a.setBinary(true) },
		cmdToggleMode: func() { a.setBinary(!a.binary) },
	}
	for id, fn := range commands {
		on.WmCommandAccelMenu(id, func() {
			if a.busy && id != cmdExit {
				beep()
				return
			}
			fn()
		})
	}
}

const wmStart = co.WM(wmUser + 100)

// start handles the command line.
func (a *App) start(opts Options) {
	if opts.Binary {
		a.setBinary(true)
	}
	if opts.Path == "" {
		return
	}
	path := opts.Path
	if abs, err := filepath.Abs(path); err == nil {
		path = abs
	}
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if filepath.Ext(path) == "" {
			if _, err := os.Stat(path + ".txt"); err == nil {
				path += ".txt"
			}
		}
	}
	if _, err := os.Stat(path); os.IsNotExist(err) && !opts.Print {
		ret, _ := a.wnd.Hwnd().MessageBox(
			fmt.Sprintf("Cannot find the %s file.\n\nDo you want to create a new file?", path),
			appName, co.MB_YESNOCANCEL|co.MB_ICONEXCLAMATION)
		switch ret {
		case co.ID_YES:
			if f, err := os.Create(path); err == nil {
				f.Close()
				a.open(path)
			} else {
				a.fileError("create", path, err)
			}
		case co.ID_CANCEL:
			a.wnd.Hwnd().PostMessage(co.WM_CLOSE, 0, 0)
		}
		return
	}
	a.openThen(path, func() {
		if opts.Print {
			a.printNow()
			a.wnd.Hwnd().PostMessage(co.WM_CLOSE, 0, 0)
		}
	})
}

// ---- window ----

// menuLine is the height of the line between the menu bar and the text in
// dark mode, at the top of the client area. In light mode Windows draws the
// lines under the menu bar.
func (a *App) menuLine() int32 {
	if ownBar {
		return 1
	}
	return 0
}

func (a *App) layout() {
	rc, _ := a.wnd.Hwnd().GetClientRect()
	top := a.menuLine()
	h := rc.Bottom - top
	if a.cfg.StatusBar {
		sr, _ := a.sb.Hwnd().GetWindowRect()
		h -= sr.Bottom - sr.Top
	}
	a.ed.Hwnd().SetWindowPos(0, win.POINT{Y: top}, win.SIZE{Cx: rc.Right, Cy: max(0, h)}, co.SWP_NOZORDER|co.SWP_NOACTIVATE)
}

// restoreWindow puts the window, which is still hidden, where it was, or a
// bit down and right of the window that opened it.
func (a *App) restoreWindow() {
	h := a.wnd.Hwnd()
	wp, err := h.GetWindowPlacement()
	if err == nil {
		rc := wp.RcNormalPosition
		if a.cfg.Width >= 200 && a.cfg.Height >= 120 {
			rc = win.RECT{Left: int32(a.cfg.X), Top: int32(a.cfg.Y), Right: int32(a.cfg.X + a.cfg.Width), Bottom: int32(a.cfg.Y + a.cfg.Height)}
		}
		if pos := strings.Split(os.Getenv("NOTE4WIN_POS"), ","); len(pos) == 2 {
			x, errX := strconv.Atoi(pos[0])
			y, errY := strconv.Atoi(pos[1])
			if errX == nil && errY == nil {
				rc.Right, rc.Bottom = int32(x)+rc.Right-rc.Left, int32(y)+rc.Bottom-rc.Top
				rc.Left, rc.Top = int32(x), int32(y)
			}
		}
		wp.RcNormalPosition = rc
		wp.ShowCmd = co.SW_HIDE
		wp.SetLength()
		h.SetWindowPlacement(&wp) // moves it back on screen when needed
	}
}

// showCommand tells how the window appears: as it was closed, unless a
// shortcut asks for minimized or maximized.
func (a *App) showCommand() co.SW {
	show := co.SW_SHOWNORMAL
	if a.cfg.Maximized {
		show = co.SW_SHOWMAXIMIZED
	}
	var si syscall.StartupInfo
	if syscall.GetStartupInfo(&si) == nil && si.Flags&1 != 0 { // STARTF_USESHOWWINDOW
		switch co.SW(si.ShowWindow) {
		case co.SW_SHOWMINIMIZED, co.SW_SHOWMAXIMIZED, co.SW_MINIMIZE, co.SW_SHOWMINNOACTIVE:
			show = co.SW(si.ShowWindow)
		}
	}
	return show
}

func (a *App) saveSettings() {
	h := a.wnd.Hwnd()
	if wp, err := h.GetWindowPlacement(); err == nil {
		rc := wp.RcNormalPosition
		a.cfg.X, a.cfg.Y = int(rc.Left), int(rc.Top)
		a.cfg.Width, a.cfg.Height = int(rc.Right-rc.Left), int(rc.Bottom-rc.Top)
		a.cfg.Maximized = h.IsZoomed()
	}
	a.cfg.WordWrap = a.ed.Wrap()
	a.cfg.MatchCase, a.cfg.WrapAround = a.find.matchCase, a.find.wrapAround
	a.cfg.Save(a.cfgDir)
}

// name is the file name for the title bar and the messages.
func (a *App) name() string {
	if a.path == "" {
		return untitled
	}
	return filepath.Base(a.path)
}

// update refreshes the title and the status bar, where they changed.
func (a *App) update() {
	title := a.name() + " - " + appName
	if a.ed.Doc().Modified() {
		title = "*" + title
	}
	if title != a.title {
		a.title = title
		a.wnd.Hwnd().SetWindowText(title)
	}
	if a.busy {
		return
	}
	if a.binary {
		mode := " INS"
		if a.ed.Overwrite() {
			mode = " OVR"
		}
		size := a.ed.Doc().Len()
		a.setStatus(
			fmt.Sprintf("  Cursor: %0*X", hexAddrDigits(size), a.ed.Caret()),
			fmt.Sprintf(" %d%%", a.ed.Zoom()),
			fmt.Sprintf(" %d bytes", size),
			mode)
		return
	}
	line, col := a.ed.LineCol()
	a.setStatus(
		fmt.Sprintf("  Ln %d, Col %d", line, col),
		fmt.Sprintf(" %d%%", a.ed.Zoom()),
		" "+a.ed.eol.String(),
		" "+a.enc.String())
}

func (a *App) setStatus(parts ...string) {
	for i, text := range parts {
		if a.status[i] != text {
			a.status[i] = text
			// Not windigo's SetText: it panics when the status bar says no,
			// which Wine's does for every text.
			a.sb.Hwnd().SendMessage(co.SB_SETTEXT, win.WPARAM(i+1), win.LPARAM(unsafe.Pointer(utf16Ptr(text))))
		}
	}
}

func (a *App) updateMenu(m win.HMENU) {
	d := a.ed.Doc()
	sel := a.ed.HasSelection()
	enableItems(m, d.CanUndo(), cmdUndo)
	enableItems(m, sel, cmdCopy)
	enableItems(m, a.ed.CanDelete(), cmdCut, cmdDelete)
	enableItems(m, !a.binary, cmdTimeDate, cmdWordWrap)
	m.CheckMenuItemByCmd(!a.binary, cmdTextMode)
	m.CheckMenuItemByCmd(a.binary, cmdBinaryMode)
	enableItems(m, CanPaste(), cmdPaste)
	enableItems(m, d.Len() > 0, cmdFind, cmdFindNext, cmdFindPrev)
	enableItems(m, a.ed.Zoom() < maxZoom, cmdZoomIn)
	enableItems(m, a.ed.Zoom() > minZoom, cmdZoomOut)
	m.CheckMenuItemByCmd(a.ed.Wrap(), cmdWordWrap)
	m.CheckMenuItemByCmd(a.cfg.StatusBar, cmdStatusBar)
}

// enableItems enables menu items, or greys them.
func enableItems(m win.HMENU, enable bool, ids ...uint16) {
	flags := uintptr(mfGrayed)
	if enable {
		flags = 0
	}
	for _, id := range ids {
		procEnableMenuItem.Call(uintptr(m), uintptr(id), flags)
	}
}

// contextMenu shows the menu of the edit control.
func (a *App) contextMenu(pt win.POINT) {
	m, _ := win.CreatePopupMenu()
	defer m.DestroyMenu()
	appendMenu(m, mfString, uintptr(cmdUndo), "&Undo")
	appendMenu(m, mfSeparator, 0, "")
	appendMenu(m, mfString, uintptr(cmdCut), "Cu&t")
	appendMenu(m, mfString, uintptr(cmdCopy), "&Copy")
	appendMenu(m, mfString, uintptr(cmdPaste), "&Paste")
	appendMenu(m, mfString, uintptr(cmdDelete), "&Delete")
	appendMenu(m, mfSeparator, 0, "")
	appendMenu(m, mfString, uintptr(cmdSelectAll), "Select &All")
	a.updateMenu(m)
	enableItems(m, a.ed.Doc().Len() > 0, cmdSelectAll)
	m.TrackPopupMenu(co.TPM_RIGHTBUTTON, int(pt.X), int(pt.Y), a.wnd.Hwnd())
}

// ---- commands ----

func (a *App) newWindow() {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	cmd := exec.Command(exe)
	if rc, err := a.wnd.Hwnd().GetWindowRect(); err == nil && !a.wnd.Hwnd().IsZoomed() {
		step := int32(ui.DpiX(26))
		cmd.Env = append(os.Environ(), fmt.Sprintf("NOTE4WIN_POS=%d,%d", rc.Left+step, rc.Top+step))
	}
	a.saveSettings() // the new window starts with the same size and font
	if err := cmd.Start(); err != nil {
		a.wnd.Hwnd().MessageBox("Cannot open a new window.\n\n"+err.Error(), appName, co.MB_ICONERROR)
	}
}

// timeDate inserts the time and the date, in the short formats of Windows.
func (a *App) timeDate() {
	if a.binary {
		beep()
		return
	}
	a.ed.Insert([]byte(localeFormat(procGetTimeFormatW, 0x0002)+" "+localeFormat(procGetDateFormatW, 0x0001)), doc.Other) // TIME_NOSECONDS, DATE_SHORTDATE
}

func localeFormat(proc *windows.LazyProc, flags uintptr) string {
	var buf [128]uint16
	n, _, _ := proc.Call(0x0400, flags, 0, 0, uintptr(unsafe.Pointer(&buf[0])), uintptr(len(buf))) // LOCALE_USER_DEFAULT
	if n == 0 {
		return ""
	}
	return syscall.UTF16ToString(buf[:n])
}

func (a *App) toggleWrap() {
	if a.binary {
		beep()
		return
	}
	a.ed.SetWrap(!a.ed.Wrap())
	a.ed.reveal()
}

// setBinary changes between the text mode and the binary mode. A UTF-8
// document has the bytes of its file, so only the view changes and the
// edits and the undo list stay. In another encoding the text is a
// conversion of the file, which is then read again in the other mode; the
// caret goes to the same place in it.
func (a *App) setBinary(binary bool) {
	if binary == a.binary {
		return
	}
	if a.enc == doc.UTF8 || a.path == "" {
		a.binary = binary
		a.ed.SetHex(binary)
		a.showFindMode()
		a.update()
		return
	}
	if !a.confirmSave() {
		return
	}
	path := a.path
	a.load(path, binary, false, func(d *doc.Doc, f *doc.File, enc doc.Encoding) {
		restore := waitCursor()
		old, caret := a.ed.Doc(), a.ed.Caret()
		if binary {
			caret = d.BOMSize() + old.RawOffset(caret, a.enc)
		} else {
			caret = old.TextOffset(caret, enc)
		}
		restore()
		a.binary = binary
		a.ed.SetHex(binary)
		a.setDoc(d, f, path, enc)
		a.ed.GoTo(caret)
		a.showFindMode()
	})
}

func (a *App) toggleStatusBar() {
	a.cfg.StatusBar = !a.cfg.StatusBar
	show := co.SW_HIDE
	if a.cfg.StatusBar {
		show = co.SW_SHOW
	}
	a.sb.Hwnd().ShowWindow(show)
	a.layout()
}

const (
	minZoom = 10
	maxZoom = 500
)

func (a *App) zoom(percent int) {
	percent = max(minZoom, min(percent, maxZoom))
	if percent == a.ed.Zoom() {
		return
	}
	a.ed.SetFont(a.ed.lf, percent)
	a.ed.reveal()
	a.update()
}

// chooseFont shows the Font dialog.
func (a *App) chooseFont() {
	lf := a.ed.lf
	cf := struct {
		size      uint32
		owner     uintptr
		hdc       uintptr
		logFont   *logFont
		pointSize int32
		flags     uint32
		color     uint32
		custData  uintptr
		hook      uintptr
		template  *uint16
		instance  uintptr
		style     *uint16
		fontType  uint16
		_         uint16
		sizeMin   int32
		sizeMax   int32
	}{owner: uintptr(a.wnd.Hwnd()), logFont: &lf, flags: 0x00000001 | 0x00000040 | 0x01000000} // CF_SCREENFONTS | CF_INITTOLOGFONTSTRUCT | CF_NOVERTFONTS
	cf.size = uint32(unsafe.Sizeof(cf))
	if r, _, _ := procChooseFontW.Call(uintptr(unsafe.Pointer(&cf))); r == 0 {
		return
	}
	a.cfg.FontFace = lf.face()
	a.cfg.FontSize = int(cf.pointSize)
	a.cfg.FontWeight = int(lf.Weight)
	a.cfg.FontItalic = lf.Italic != 0
	a.cfg.FontCharSet = int(lf.CharSet)
	a.ed.SetFont(a.fontFromSettings(), a.ed.Zoom())
	a.ed.reveal()
	a.saveSettings()
}

// fileError shows why a file could not be read or written.
func (a *App) fileError(verb, path string, err error) {
	reason := err.Error()
	if pe, ok := err.(*os.PathError); ok {
		reason = pe.Err.Error()
	}
	a.wnd.Hwnd().MessageBox(fmt.Sprintf("Cannot %s the %s file.\n\n%s", verb, path, reason), appName, co.MB_ICONEXCLAMATION)
}
