package autoruntime

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
)

const (
	MaximumServingLineBytes = 64 << 10
	MaximumServingTailBytes = 64 << 10
)

// servingLogs consumes an arbitrary number of complete bounded lines. It keeps
// only a diagnostic tail and semantic facts, never a lifetime transcript. The
// channels remain separate until complete rows have been assembled.
type servingLogs struct {
	mu            sync.Mutex
	channels      [2]servingLine
	tail          []byte
	cloudDisabled bool
	fault         error
	facts         allocationFacts
	sequence      uint64
}

type servingLine struct {
	data    []byte
	discard bool
}

type servingWriter struct {
	logs    *servingLogs
	channel int
}

func (logs *servingLogs) writer(channel int) io.Writer {
	return servingWriter{logs: logs, channel: channel}
}

func (writer servingWriter) Write(data []byte) (int, error) {
	logs := writer.logs
	logs.mu.Lock()
	defer logs.mu.Unlock()
	logs.appendTail(data)
	length := len(data)
	line := &logs.channels[writer.channel]
	for len(data) > 0 {
		end := bytes.IndexByte(data, '\n')
		complete := end >= 0
		if !complete {
			end = len(data)
		}
		logs.fragment(line, data[:end], complete)
		data = data[end:]
		if complete {
			data = data[1:]
		}
	}
	// Always drain the child pipes. Ownership checks surface the sticky fault;
	// returning a write error here could strand an otherwise stoppable child.
	return length, nil
}

func (logs *servingLogs) fragment(line *servingLine, data []byte, complete bool) {
	if !line.discard {
		if len(data) > MaximumServingLineBytes-len(line.data) {
			logs.fault = errors.New("owned runtime diagnostic line exceeds 64 KiB; allocation facts are unavailable")
			line.discard = true
			line.data = line.data[:0]
		} else {
			line.data = append(line.data, data...)
		}
	}
	if !complete {
		return
	}
	if !line.discard {
		text := strings.TrimSuffix(string(line.data), "\r")
		if strings.Contains(text, "Ollama cloud disabled: true") {
			logs.cloudDisabled = true
		}
		if logs.facts.observe(text) {
			logs.sequence++
		}
	}
	line.data, line.discard = line.data[:0], false
}

func (logs *servingLogs) appendTail(data []byte) {
	if len(data) >= MaximumServingTailBytes {
		logs.tail = append(logs.tail[:0], data[len(data)-MaximumServingTailBytes:]...)
		return
	}
	if excess := len(logs.tail) + len(data) - MaximumServingTailBytes; excess > 0 {
		copy(logs.tail, logs.tail[excess:])
		logs.tail = logs.tail[:len(logs.tail)-excess]
	}
	logs.tail = append(logs.tail, data...)
}

func (logs *servingLogs) beginPoint() {
	logs.mu.Lock()
	defer logs.mu.Unlock()
	logs.facts = allocationFacts{}
	for i := range logs.channels {
		line := &logs.channels[i]
		if len(line.data) > 0 {
			line.data, line.discard = line.data[:0], true
		}
	}
}

func (logs *servingLogs) status() (bool, error) {
	logs.mu.Lock()
	defer logs.mu.Unlock()
	return logs.cloudDisabled, logs.fault
}

func (logs *servingLogs) allocation() (string, uint64, error) {
	logs.mu.Lock()
	defer logs.mu.Unlock()
	if logs.fault != nil {
		return "", logs.sequence, logs.fault
	}
	if logs.facts.invalid {
		return "", logs.sequence, fmt.Errorf("owned runtime allocation backend is unknown or mixed: %s", logs.facts.failure)
	}
	return logs.facts.accel(), logs.sequence, nil
}
