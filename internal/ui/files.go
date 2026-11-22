//go:build windows

package ui

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"

	"github.com/mevdschee/text4win/internal/doc"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"
	"golang.org/x/sys/windows"
)

// confirmSave asks what to do with unsaved changes. It returns false when
// the user cancels, or when saving fails.
func (a *App) confirmSave() bool {
	if !a.ed.Doc().Modified() {
		return true
	}
	name := a.path
	if name == "" {
		name = untitled
	}
	const idSave, idDont = 100, 101
	ret, err := win.TaskDialogIndirect(&win.TASKDIALOGCONFIG{
		HwndParent:      a.wnd.Hwnd(),
		Flags:           co.TDF_ALLOW_DIALOG_CANCELLATION | co.TDF_POSITION_RELATIVE_TO_WINDOW,
		WindowTitle:     appName,
		MainInstruction: "Do you want to save changes to " + name + "?",
		Buttons: []win.TASKDIALOG_BUTTON{
			{Id: idSave, Text: "&Save"},
			{Id: idDont, Text: "Do&n't Save"},
			{Id: co.ID_CANCEL, Text: "Cancel"},
		},
		DefaultButtonId: idSave,
	})
	if err != nil { // no task dialogs: the plain question
		ret, _ = a.wnd.Hwnd().MessageBox("Do you want to save changes to "+name+"?", appName, co.MB_YESNOCANCEL|co.MB_ICONEXCLAMATION)
		switch ret {
		case co.ID_YES:
			ret = idSave
		case co.ID_NO:
			ret = idDont
		}
	}
	switch ret {
	case idSave:
		return a.save()
	case idDont:
		return true
	}
	return false
}

// setDoc shows a document and releases the file of the one before.
func (a *App) setDoc(d *doc.Doc, file *doc.File, path string, enc doc.Encoding) {
	old := a.file
	a.ed.SetDoc(d, d.EOL())
	a.file, a.path, a.enc = file, path, enc
	if old != nil && old != file {
		old.Close()
	}
	a.update()
}

func (a *App) newFile() {
	if !a.confirmSave() {
		return
	}
	a.setDoc(doc.New(nil, nil, nil), nil, "", doc.UTF8)
}

func (a *App) openDialog() {
	if !a.confirmSave() {
		return
	}
	path, ok := openFileDialog(a.wnd.Hwnd(), a.path)
	a.ed.Hwnd().SetFocus()
	if ok {
		a.open(path)
	}
}

func (a *App) open(path string) { a.openThen(path, nil) }

// openThen opens a file and calls then when it is shown. Small files are
// read at once. Large ones are mapped and indexed in the background, with
// the progress in the status bar; closing the window cancels that.
func (a *App) openThen(path string, then func()) {
	a.load(path, a.binary, false, func(d *doc.Doc, f *doc.File, enc doc.Encoding) {
		a.setDoc(d, f, path, enc)
		if !a.binary {
			a.logFile()
		}
		if then != nil {
			then()
		}
	})
}

// load opens a file and hands the document to done, on the UI thread.
// Nothing is changed when it fails. With binary set the document is the
// bytes of the file as they are. With wait set a large file is not loaded
// in the background: done is called before load returns.
func (a *App) load(path string, binary, wait bool, done func(*doc.Doc, *doc.File, doc.Encoding)) {
	read := doc.Load
	if binary {
		read = doc.LoadRaw
	}
	st, err := os.Stat(path)
	if err == nil && st.IsDir() {
		err = &os.PathError{Op: "open", Path: path, Err: windows.ERROR_ACCESS_DENIED}
	}
	if err != nil {
		a.fileError("open", path, err)
		return
	}
	if wait || st.Size() < doc.MapLimit {
		restore := waitCursor()
		f, err := doc.Open(path)
		if err != nil {
			restore()
			a.fileError("open", path, err)
			return
		}
		d, enc := read(f.Data, !f.Mapped, nil, nil)
		restore()
		done(d, f, enc)
		return
	}

	a.busy = true
	a.stop = new(atomic.Bool)
	a.loaded.Store(0)
	a.total = st.Size()
	a.ed.SetLocked(true)
	a.setStatus("  Opening " + filepath.Base(path) + "...")
	a.wnd.Hwnd().SetTimer(timerProgress, 100)
	stop := a.stop
	go func() {
		var d *doc.Doc
		var enc doc.Encoding
		f, err := doc.Open(path)
		if err == nil {
			d, enc = read(f.Data, false, func(n int64) {
				for {
					old := a.loaded.Load()
					if n <= old || a.loaded.CompareAndSwap(old, n) {
						return
					}
				}
			}, stop)
		}
		a.wnd.UiThread(func() {
			a.wnd.Hwnd().KillTimer(timerProgress)
			a.busy = false
			a.ed.SetLocked(false)
			a.status = [4]string{}
			switch {
			case err != nil:
				a.update()
				a.fileError("open", path, err)
			case d == nil: // cancelled by closing the window
				f.Close()
				a.wnd.Hwnd().PostMessage(co.WM_CLOSE, 0, 0)
			default:
				done(d, f, enc)
			}
		})
	}()
}

func (a *App) showProgress() {
	if !a.busy || a.total == 0 {
		return
	}
	a.setStatus("", fmt.Sprintf(" %d%%", min(100, a.loaded.Load()*100/a.total)))
}

// logFile is Notepad's log feature: a file that starts with .LOG gets the
// time and date added at its end each time it is opened.
func (a *App) logFile() {
	d := a.ed.Doc()
	if !bytes.Equal(d.Bytes(0, 4), []byte(".LOG")) {
		return
	}
	eol := string(a.ed.eol.Bytes())
	stamp := localeFormat(procGetTimeFormatW, 0x0002) + " " + localeFormat(procGetDateFormatW, 0x0001)
	a.ed.moveTo(d.Len(), false)
	a.ed.Insert([]byte(eol+stamp+eol), doc.Other)
}

// save writes the document to its file, asking for a name when it has none.
func (a *App) save() bool {
	if a.path == "" {
		return a.saveAs()
	}
	return a.write(a.path, a.enc)
}

func (a *App) saveAs() bool {
	name := a.path
	if name == "" {
		name = "*.txt"
		if a.binary {
			name = "*.*"
		}
	}
	path, enc, ok := saveFileDialog(a.wnd.Hwnd(), name, a.enc, a.binary)
	a.ed.Hwnd().SetFocus()
	if !ok {
		return false
	}
	if !a.binary && enc == doc.ANSI && !a.ed.Doc().FitsANSI() {
		ret, _ := a.wnd.Hwnd().MessageBox(path+"\n"+
			"This file contains characters in Unicode format which will be lost if you save this file as an ANSI encoded text file. "+
			"To keep the Unicode information, click Cancel below and then select one of the Unicode options from the Encoding drop down list. Continue?",
			appName, co.MB_OKCANCEL|co.MB_ICONEXCLAMATION)
		if ret != co.ID_OK {
			return false
		}
	}
	return a.write(path, enc)
}

// write saves the document.
//
// A document that was read into memory is written over the file itself,
// like Notepad does, which keeps the file's permissions, links and streams.
// A memory mapped document cannot be written like that, because the text
// that is not edited still lives in the file. It goes to a temporary file
// next to the target, which then replaces the target; after that the
// document is mapped again from the new file, with an empty undo list.
//
// In binary mode the document is the bytes of the file, which are written
// as they are; enc is then only what the text mode would read them as.
func (a *App) write(path string, enc doc.Encoding) bool {
	d := a.ed.Doc()
	as := enc
	if a.binary {
		as = doc.UTF8
	}
	restore := waitCursor()
	defer restore()
	if a.file == nil || !a.file.Mapped {
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
		if err == nil {
			err = d.WriteTo(f, as)
			if cerr := f.Close(); err == nil {
				err = cerr
			}
		}
		if err != nil {
			a.fileError("save", path, err)
			return false
		}
		d.SetSaved()
		a.path, a.enc = path, enc
		a.update()
		return true
	}

	tmp := path + ".text4win.tmp"
	os.Remove(tmp) // left behind by a save that failed
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o666)
	if err == nil {
		err = d.WriteTo(f, as)
		if cerr := f.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			os.Remove(tmp)
		}
	}
	if err != nil {
		a.fileError("save", path, err)
		return false
	}
	// From here the old document is gone: its text is in the temporary file.
	top, anchor, caret, eol := a.ed.top, a.ed.anchor, a.ed.caret, a.ed.eol
	sameText := a.binary || enc == a.enc || (enc != doc.ANSI && a.enc != doc.ANSI)
	a.setDoc(doc.New(nil, nil, nil), nil, a.path, a.enc)
	if err := replaceFile(tmp, path); err != nil {
		a.fileError("save", path, err)
		path = tmp // the text is still there
	}
	a.load(path, a.binary, true, func(nd *doc.Doc, nf *doc.File, _ doc.Encoding) {
		a.setDoc(nd, nf, path, enc)
		a.ed.SetEOL(eol)
		if sameText { // the same bytes, so the same offsets
			a.ed.top, a.ed.anchor, a.ed.caret = top, anchor, caret
			a.ed.relayout()
			a.update()
		}
	})
	return !strings.HasSuffix(path, ".text4win.tmp")
}

// replaceFile moves tmp over path. When path exists it keeps its
// attributes and permissions.
func replaceFile(tmp, path string) error {
	t, _ := windows.UTF16PtrFromString(tmp)
	p, _ := windows.UTF16PtrFromString(path)
	if _, err := os.Stat(path); err == nil {
		r, _, e := procReplaceFileW.Call(uintptr(ptr(p)), uintptr(ptr(t)), 0, 0x2, 0, 0) // REPLACEFILE_IGNORE_MERGE_ERRORS
		if r != 0 {
			return nil
		}
		_ = e // fall back to a plain move
	}
	return windows.MoveFileEx(t, p, windows.MOVEFILE_REPLACE_EXISTING|windows.MOVEFILE_WRITE_THROUGH)
}
