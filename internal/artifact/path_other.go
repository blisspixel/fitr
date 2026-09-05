//go:build !windows

package artifact

import "os"

func ambiguousFile(info os.FileInfo) bool { return info.Mode()&(os.ModeSymlink|os.ModeIrregular) != 0 }
