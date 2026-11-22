//go:build !windows

package doc

import "os"

// Open reads a file.
func Open(path string) (*File, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return &File{Data: data}, nil
}

// Close releases the file.
func (f *File) Close() { f.Data = nil }
