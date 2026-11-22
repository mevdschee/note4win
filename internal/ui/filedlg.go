//go:build windows

package ui

import (
	"path/filepath"
	"strings"
	"syscall"
	"unsafe"

	"github.com/mevdschee/text4win/internal/doc"
	"github.com/rodrigocfd/windigo/co"
	"github.com/rodrigocfd/windigo/win"
	"github.com/rodrigocfd/windigo/x/cosh"
	"github.com/rodrigocfd/windigo/x/winsh"
)

var fileTypes = []winsh.COMDLG_FILTERSPEC{
	{Name: "Text Documents (*.txt)", Spec: "*.txt"},
	{Name: "All Files (*.*)", Spec: "*.*"},
}

// comCall calls method index of a COM object.
func comCall(obj uintptr, index int, args ...uintptr) uintptr {
	// obj points to a pointer to the method table (the casts keep go vet quiet).
	table := **(***[64]uintptr)(unsafe.Pointer(&obj))
	r, _, _ := syscall.SyscallN(table[index], append([]uintptr{obj}, args...)...)
	return r
}

// startIn opens the dialog in the folder of the current file.
func startIn(rel *win.OleReleaser, dlg *winsh.IFileDialog, current string) {
	if current == "" || strings.HasPrefix(current, "*") {
		return
	}
	var folder *winsh.IShellItem
	if winsh.SHCreateItemFromParsingName(rel, filepath.Dir(current), &folder) == nil {
		dlg.SetFolder(folder)
	}
}

func dialogResult(rel *win.OleReleaser, dlg *winsh.IFileDialog, owner win.HWND) (string, bool) {
	if ok, _ := dlg.Show(owner); !ok {
		return "", false
	}
	item, err := dlg.GetResult(rel)
	if err != nil {
		return "", false
	}
	path, err := item.GetDisplayName(cosh.SIGDN_FILESYSPATH)
	return path, err == nil && path != ""
}

// openFileDialog shows the Open dialog.
func openFileDialog(owner win.HWND, current string) (string, bool) {
	rel := win.NewOleReleaser()
	defer rel.Release()
	var dlg *winsh.IFileOpenDialog
	if err := win.CoCreateInstance(rel, &cosh.CLSID_FileOpenDialog, nil, co.CLSCTX_INPROC_SERVER, &dlg); err != nil {
		return "", false
	}
	opts, _ := dlg.GetOptions()
	dlg.SetOptions(opts | cosh.FOS_FORCEFILESYSTEM | cosh.FOS_FILEMUSTEXIST)
	dlg.SetFileTypes(fileTypes)
	dlg.SetDefaultExtension("txt")
	startIn(rel, &dlg.IFileDialog, current)
	return dialogResult(rel, &dlg.IFileDialog, owner)
}

// IFileDialogCustomize, which windigo does not have: its ID and the slots
// of the methods used.
var iidFileDialogCustomize = [16]byte{0x1a, 0xd2, 0xfd, 0xe6, 0x3f, 0x16, 0x75, 0x49, 0x9c, 0x8c, 0xa6, 0x9f, 0x1b, 0xa3, 0x70, 0x34}

const (
	vtRelease                = 2
	vtAddComboBox            = 6
	vtAddControlItem         = 19
	vtGetSelectedControlItem = 24
	vtSetSelectedControlItem = 25
	vtStartVisualGroup       = 26
	vtEndVisualGroup         = 27

	ctlGroup = 100
	ctlCombo = 101
)

// saveFileDialog shows the Save As dialog, with Notepad's Encoding list.
// In binary mode there is no encoding to choose and no ".txt" to add.
func saveFileDialog(owner win.HWND, current string, enc doc.Encoding, binary bool) (string, doc.Encoding, bool) {
	rel := win.NewOleReleaser()
	defer rel.Release()
	var dlg *winsh.IFileSaveDialog
	if err := win.CoCreateInstance(rel, &cosh.CLSID_FileSaveDialog, nil, co.CLSCTX_INPROC_SERVER, &dlg); err != nil {
		return "", enc, false
	}
	opts, _ := dlg.GetOptions()
	dlg.SetOptions(opts | cosh.FOS_FORCEFILESYSTEM | cosh.FOS_OVERWRITEPROMPT)
	dlg.SetFileTypes(fileTypes)
	if !binary {
		dlg.SetDefaultExtension("txt")
	}
	if !strings.HasPrefix(current, "*") {
		dlg.SetFileName(filepath.Base(current))
	} else if !binary {
		dlg.SetFileName(current)
	}
	if ext := strings.ToLower(filepath.Ext(current)); binary || ext != ".txt" && !strings.HasPrefix(current, "*") {
		dlg.SetFileTypeIndex(2)
	}
	startIn(rel, &dlg.IFileDialog, current)

	var custom uintptr
	if int32(comCall(dlg.Ppvt(), 0, uintptr(unsafe.Pointer(&iidFileDialogCustomize)), uintptr(unsafe.Pointer(&custom)))) < 0 {
		custom = 0
	}
	if custom != 0 && binary {
		comCall(custom, vtRelease)
		custom = 0
	}
	if custom != 0 {
		defer comCall(custom, vtRelease)
		comCall(custom, vtStartVisualGroup, ctlGroup, uintptr(unsafe.Pointer(utf16Ptr("&Encoding:"))))
		comCall(custom, vtAddComboBox, ctlCombo)
		for _, e := range doc.Encodings {
			comCall(custom, vtAddControlItem, ctlCombo, uintptr(e), uintptr(unsafe.Pointer(utf16Ptr(e.String()))))
		}
		comCall(custom, vtSetSelectedControlItem, ctlCombo, uintptr(enc))
		comCall(custom, vtEndVisualGroup)
	}
	path, ok := dialogResult(rel, &dlg.IFileDialog, owner)
	if !ok {
		return "", enc, false
	}
	if custom != 0 {
		var selected uint32
		if int32(comCall(custom, vtGetSelectedControlItem, ctlCombo, uintptr(unsafe.Pointer(&selected)))) >= 0 && int(selected) < len(doc.Encodings) {
			enc = doc.Encoding(selected)
		}
	}
	return path, enc, true
}
