package device

import "github.com/blisspixel/fitr/internal/listen"

// readListenerConfig is the initial environment of the process bound to the
// measured endpoint. Tests replace it. Any error, including a listener that
// cannot be named, is unobserved configuration.
var readListenerConfig = readListenerEnviron

func readListenerEnviron(endpoint string) (map[string]string, error) {
	ep, err := listen.ParseEndpoint(endpoint)
	if err != nil || !ep.Local {
		return nil, listen.ErrUnobserved
	}
	pid, err := listen.ListeningPID(ep)
	if err != nil {
		return nil, err
	}
	return listen.Environ(pid)
}
