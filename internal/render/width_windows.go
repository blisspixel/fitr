//go:build windows

package render

import (
	"os"
	"syscall"
	"unsafe"
)

type coord struct{ X, Y int16 }

type smallRect struct{ Left, Top, Right, Bottom int16 }

type consoleScreenBufferInfo struct {
	Size              coord
	CursorPosition    coord
	Attributes        uint16
	Window            smallRect
	MaximumWindowSize coord
}

var procGetConsoleScreenBufferInfo = syscall.NewLazyDLL("kernel32.dll").
	NewProc("GetConsoleScreenBufferInfo")

// terminalWidth reads the visible window width, not the screen buffer width.
// Buffer width is what a console is scrolled within; on a default conhost the
// buffer is 120 while the window may be 80, and composing to the buffer would
// wrap every line.
func terminalWidth(f *os.File) (int, bool) {
	if w, ok := consoleWidth(f.Fd()); ok {
		return w, true
	}
	return 0, false
}

func consoleWidth(handle uintptr) (int, bool) {
	var info consoleScreenBufferInfo
	r, _, _ := procGetConsoleScreenBufferInfo.Call(handle, uintptr(unsafe.Pointer(&info)))
	if r == 0 {
		return 0, false
	}
	w := int(info.Window.Right-info.Window.Left) + 1
	if w <= 0 {
		return 0, false
	}
	return w, true
}
