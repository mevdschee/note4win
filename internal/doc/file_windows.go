package doc

import (
	"errors"
	"os"
	"unsafe"

	"golang.org/x/sys/windows"
)

// Open reads a file, or maps it into memory when it is MapLimit or larger.
func Open(path string) (*File, error) {
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if st.IsDir() {
		return nil, errors.New("this is a folder, not a file")
	}
	if st.Size() < MapLimit {
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		return &File{Data: data}, nil
	}
	p, err := windows.UTF16PtrFromString(path)
	if err != nil {
		return nil, err
	}
	h, err := windows.CreateFile(p, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	defer windows.CloseHandle(h) // the mapping keeps the file open
	var info windows.ByHandleFileInformation
	if err := windows.GetFileInformationByHandle(h, &info); err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	size := int64(info.FileSizeHigh)<<32 | int64(info.FileSizeLow)
	if size == 0 {
		return &File{}, nil
	}
	m, err := windows.CreateFileMapping(h, nil, windows.PAGE_READONLY, 0, 0, nil)
	if err != nil {
		return nil, &os.PathError{Op: "map", Path: path, Err: err}
	}
	addr, err := windows.MapViewOfFile(m, windows.FILE_MAP_READ, 0, 0, 0)
	if err != nil {
		windows.CloseHandle(m)
		return nil, &os.PathError{Op: "map", Path: path, Err: err}
	}
	// addr is memory outside the Go heap (the cast keeps go vet quiet).
	base := *(**byte)(unsafe.Pointer(&addr))
	return &File{
		Data:   unsafe.Slice(base, size),
		Mapped: true, handle: uintptr(m), addr: addr,
	}, nil
}

// Close releases the file. Its data must not be used afterwards.
func (f *File) Close() {
	if f.Mapped {
		windows.UnmapViewOfFile(f.addr)
		windows.CloseHandle(windows.Handle(f.handle))
		f.Mapped = false
	}
	f.Data = nil
}
