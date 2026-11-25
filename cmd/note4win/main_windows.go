// Command note4win is a Notepad for Windows that opens huge files.
package main

import (
	"fmt"
	"os"
	"runtime"
	"runtime/debug"
	"strings"

	"github.com/mevdschee/note4win/internal/ui"
	"github.com/rodrigocfd/windigo/co"
	wui "github.com/rodrigocfd/windigo/ui"
	"github.com/rodrigocfd/windigo/win"
)

// version is set at build time by release.sh (-ldflags -X main.version=...).
var version = "dev"

func main() {
	runtime.LockOSThread() // the Win32 GUI is single-threaded

	defer func() {
		if r := recover(); r != nil {
			wui.MsgError(nil, "note4win", "note4win crashed", fmt.Sprintf("%v\n\n%s", r, debug.Stack()))
			os.Exit(2)
		}
	}()

	// COM is needed for the file dialogs.
	win.CoInitializeEx(co.COINIT_APARTMENTTHREADED | co.COINIT_DISABLE_OLE1DDE)
	defer win.CoUninitialize()

	os.Exit(ui.Run(version, parseArgs(os.Args[1:])))
}

// parseArgs reads the command line like Notepad: an optional file, "/p" to
// print it, and "/a" and "/w" (ANSI and Unicode), which are accepted and
// ignored because the encoding is detected. "/b" is not Notepad's: it starts
// in binary mode.
func parseArgs(args []string) ui.Options {
	var opts ui.Options
	var words []string
	for _, arg := range args {
		if len(arg) == 2 && (arg[0] == '/' || arg[0] == '-') && opts.Path == "" && len(words) == 0 {
			switch strings.ToLower(arg[1:]) {
			case "p":
				opts.Print = true
				continue
			case "b":
				opts.Binary = true
				continue
			case "a", "w":
				continue
			}
		}
		words = append(words, arg)
	}
	// "notepad my file.txt" works without quotes in Notepad.
	opts.Path = strings.Join(words, " ")
	return opts
}
