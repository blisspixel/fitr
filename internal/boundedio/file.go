// Package boundedio provides size-limited reads for local files whose size is
// not controlled by fitr. Limits keep malformed inputs and growing logs from
// turning a diagnostic or import into unbounded memory use.
package boundedio

import (
	"fmt"
	"io"
	"os"
)

// ReadFile reads one regular file and rejects content larger than limit.
func ReadFile(path string, limit int64) ([]byte, error) {
	if path == "" || limit <= 0 {
		return nil, fmt.Errorf("invalid bounded file read")
	}
	if err := requireRegular(path, limit, true); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if err := requireOpenRegular(f); err != nil {
		return nil, err
	}
	b, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if int64(len(b)) > limit {
		return nil, fmt.Errorf("file exceeds %d bytes", limit)
	}
	return b, nil
}

// ReadTail reads at most the final limit bytes of one regular file.
func ReadTail(path string, limit int64) ([]byte, error) {
	if path == "" || limit <= 0 {
		return nil, fmt.Errorf("invalid bounded file read")
	}
	if err := requireRegular(path, limit, false); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("path is not a regular file")
	}
	if info.Size() > limit {
		if _, err := f.Seek(-limit, io.SeekEnd); err != nil {
			return nil, err
		}
	}
	return io.ReadAll(io.LimitReader(f, limit))
}

// ReadEdges reads one regular file in full when it fits. For a larger file it
// returns bounded samples from the beginning and end, separated by a newline.
func ReadEdges(path string, limit int64) ([]byte, error) {
	if path == "" || limit <= 0 {
		return nil, fmt.Errorf("invalid bounded file read")
	}
	if limit < 3 {
		return ReadTail(path, limit)
	}
	if err := requireRegular(path, limit, false); err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() {
		return nil, fmt.Errorf("path is not a regular file")
	}
	if info.Size() <= limit {
		return io.ReadAll(io.LimitReader(f, limit))
	}
	headLimit := (limit - 1) / 2
	tailLimit := limit - 1 - headLimit
	head, err := io.ReadAll(io.LimitReader(f, headLimit))
	if err != nil {
		return nil, err
	}
	if _, err := f.Seek(-tailLimit, io.SeekEnd); err != nil {
		return nil, err
	}
	tail, err := io.ReadAll(io.LimitReader(f, tailLimit))
	if err != nil {
		return nil, err
	}
	out := make([]byte, 0, len(head)+1+len(tail))
	out = append(out, head...)
	out = append(out, '\n')
	out = append(out, tail...)
	return out, nil
}

func requireRegular(path string, limit int64, rejectOversize bool) error {
	info, err := os.Stat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
	}
	if rejectOversize && info.Size() > limit {
		return fmt.Errorf("file exceeds %d bytes", limit)
	}
	return nil
}

func requireOpenRegular(f *os.File) error {
	info, err := f.Stat()
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("path is not a regular file")
	}
	return nil
}
