package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"time"
)

const maxInFlight = 4

type evidenceSource interface {
	list(context.Context) (any, error)
	review(context.Context, string) (any, error)
	status(context.Context, string) (any, error)
}

type pending struct {
	ctx    context.Context
	cancel context.CancelFunc
}

type completed struct {
	key   string
	reply response
}
type inputLine struct {
	data []byte
	err  error
}

type server struct {
	source  evidenceSource
	version string
	active  map[string]pending
	done    chan completed
	window  time.Time
	calls   int
}

// Serve owns input until return and closes it on cancellation. The result root
// comes from trusted process configuration, never from an MCP request. Output
// contains protocol messages only. No files or remote services are written.
func Serve(ctx context.Context, input io.ReadCloser, output io.Writer, results, version string) error {
	source, err := newLocalEvidence(results)
	if err != nil {
		return errors.New("MCP evidence configuration is invalid")
	}
	return serve(ctx, input, output, source, version)
}

func serve(ctx context.Context, input io.ReadCloser, output io.Writer, source evidenceSource, version string) error {
	ctx, stop := context.WithCancel(ctx)
	defer stop()
	defer input.Close()
	s := server{source: source, version: version, active: map[string]pending{}, done: make(chan completed, maxInFlight)}
	defer s.cancelAll()
	lines := make(chan inputLine, 1)
	go readLines(ctx, input, lines)
	encoder := json.NewEncoder(output)
	var draining <-chan time.Time
	for lines != nil || len(s.active) > 0 {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-draining:
			return errors.New("MCP shutdown exceeded its drain limit")
		case line, open := <-lines:
			if !open {
				lines = nil
				draining = time.After(5 * time.Second)
				continue
			}
			if line.err != nil {
				return errors.New("MCP input failed or exceeded 64 KiB")
			}
			if reply := s.accept(ctx, line.data); reply != nil {
				if err := encoder.Encode(reply); err != nil {
					return errors.New("MCP output failed")
				}
			}
		case result := <-s.done:
			work := s.active[result.key]
			delete(s.active, result.key)
			cancelled := work.ctx.Err() != nil
			work.cancel()
			if !cancelled {
				if err := encoder.Encode(result.reply); err != nil {
					return errors.New("MCP output failed")
				}
			}
		}
	}
	// A select chooses uniformly among ready cases, so a cancelled context and
	// a closed input are both live at the same moment. Taking the closed input
	// first ends the loop, and returning nil there would report a clean stop
	// for a server that was actually interrupted.
	return ctx.Err()
}

func readLines(ctx context.Context, input io.Reader, lines chan<- inputLine) {
	defer close(lines)
	scanner := bufio.NewScanner(input)
	scanner.Buffer(make([]byte, 4096), maxMessageBytes+1)
	for scanner.Scan() {
		line := inputLine{data: append([]byte(nil), scanner.Bytes()...)}
		select {
		case lines <- line:
		case <-ctx.Done():
			return
		}
	}
	if err := scanner.Err(); err != nil {
		select {
		case lines <- inputLine{err: err}:
		case <-ctx.Done():
		}
	}
}

func (s *server) cancelAll() {
	for _, work := range s.active {
		work.cancel()
	}
}

func (s *server) accept(ctx context.Context, line []byte) *response {
	req, invalid := decodeRequest(line)
	if invalid != nil {
		return invalid
	}
	if req.id == nil {
		s.notification(req)
		return nil
	}
	if invalid := validateMetadata(req); invalid != nil {
		return invalid
	}
	if _, exists := s.active[req.key]; exists {
		r := failure(req.id, -32600, "Request ID is already in flight")
		return &r
	}
	if len(s.active) >= maxInFlight {
		r := failure(req.id, -32603, "Server busy; retry after outstanding requests finish")
		return &r
	}
	if req.method != "tools/call" {
		r := s.protocol(req)
		return &r
	}
	if !s.allowCall(time.Now()) {
		r := failure(req.id, -32603, "Tool rate limit reached; retry after one minute")
		return &r
	}
	workCtx, cancel := context.WithCancel(ctx)
	s.active[req.key] = pending{ctx: workCtx, cancel: cancel}
	go func() { s.done <- completed{key: req.key, reply: s.call(workCtx, req)} }()
	return nil
}

func (s *server) notification(req request) {
	if req.method != "notifications/cancelled" || !onlyKeys(req.params, "requestId", "reason", "_meta") {
		return
	}
	if reason, exists := req.params["reason"]; exists {
		var text *string
		if json.Unmarshal(reason, &text) != nil || text == nil {
			return
		}
	}
	key, ok := idKey(req.params["requestId"])
	if !ok {
		return
	}
	if work, exists := s.active[key]; exists {
		work.cancel()
	}
}

func (s *server) allowCall(now time.Time) bool {
	if now.Sub(s.window) >= time.Minute {
		s.window, s.calls = now, 0
	}
	if s.calls >= 60 {
		return false
	}
	s.calls++
	return true
}

func (s *server) complete(id json.RawMessage, result map[string]any) response {
	result["resultType"] = "complete"
	result["_meta"] = map[string]any{"io.modelcontextprotocol/serverInfo": map[string]string{"name": "fitr", "version": s.version}}
	return response{JSONRPC: "2.0", ID: id, Result: result}
}

func (s *server) protocol(req request) response {
	switch req.method {
	case "server/discover":
		if !onlyKeys(req.params, "_meta") {
			return failure(req.id, -32602, "Discovery accepts only metadata")
		}
		return s.complete(req.id, map[string]any{
			"supportedVersions": []string{ProtocolVersion}, "capabilities": map[string]any{"tools": map[string]any{}},
			"instructions": "Read-only local battery screening evidence. An exploration lead does not authorize adoption. No model or harness execution is available.",
			"ttlMs":        60000, "cacheScope": "public",
		})
	case "tools/list":
		if !onlyKeys(req.params, "_meta", "cursor") || req.params["cursor"] != nil {
			return failure(req.id, -32602, "Tool catalog is one page; omit cursor")
		}
		return s.complete(req.id, map[string]any{"tools": catalog(), "ttlMs": 60000, "cacheScope": "public"})
	default:
		return failure(req.id, -32601, "Method not supported by this read-only profile")
	}
}
