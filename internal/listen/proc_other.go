//go:build !linux && !windows

package listen

// ListeningPID, Environ, and OpenLogs have no proven read-only mechanism on
// this OS. Callers record unobserved rather than guessing from a process
// name or from a log path fitr was not shown.
func ListeningPID(Endpoint) (int, error) { return 0, ErrUnobserved }

func Environ(int) (map[string]string, error) { return nil, ErrUnobserved }

func OpenLogs(int) ([]string, error) { return nil, ErrUnobserved }
