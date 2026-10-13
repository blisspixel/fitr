package ollama

import (
	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/listen"
)

// openLogsForAccel lists regular log files the listening process has open.
// Tests replace it. The default server log path is not a member of this list
// unless that process actually has the file open.
var openLogsForAccel = openListenerLogs

func openListenerLogs(endpoint string) ([]string, error) {
	ep, err := listen.ParseEndpoint(endpoint)
	if err != nil || !ep.Local {
		return nil, listen.ErrUnobserved
	}
	pid, err := listen.ListeningPID(ep)
	if err != nil {
		return nil, err
	}
	return listen.OpenLogs(pid)
}

// libraryFromOpenLogs returns the last library= in the tail of the listener's
// open logs when every such tail names the same library. A library token that
// survives only in the head of a long file is an older process. Two logs that
// disagree are unobserved, not a second backend.
func libraryFromOpenLogs(paths []string) string {
	found := false
	library := ""
	for _, path := range paths {
		b, err := boundedio.ReadTail(path, maxServerLogTail)
		if err != nil {
			return ""
		}
		all := serverLibraryPattern.FindAllStringSubmatch(string(b), -1)
		if len(all) == 0 {
			continue
		}
		last := all[len(all)-1][1]
		if found && last != library {
			return ""
		}
		library, found = last, true
	}
	if !found {
		return ""
	}
	return library
}
