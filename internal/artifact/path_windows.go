package artifact

import (
	"os"
	"syscall"
)

func ambiguousFile(info os.FileInfo) bool {
	attributes, ok := info.Sys().(*syscall.Win32FileAttributeData)
	return info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 || (ok && attributes.FileAttributes&syscall.FILE_ATTRIBUTE_REPARSE_POINT != 0)
}
