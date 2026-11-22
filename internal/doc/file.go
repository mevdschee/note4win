package doc

// MapLimit is the file size from which a file is memory mapped instead of
// read. A mapped file needs no memory for its text, but it stays open: other
// programs cannot delete or shrink it until the document is closed.
var MapLimit int64 = 256 << 20

// File holds the contents of an opened file.
type File struct {
	Data   []byte
	Mapped bool
	handle uintptr
	addr   uintptr
}
