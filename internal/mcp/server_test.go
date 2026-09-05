package mcp

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
)

type blockingSource struct {
	started chan struct{}
	release chan struct{}
}

func (source blockingSource) list(ctx context.Context) (any, error) {
	close(source.started)
	select {
	case <-source.release:
		return (fixtureSource{}).list(ctx)
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
func (source blockingSource) review(ctx context.Context, _ string) (any, error) {
	return source.list(ctx)
}

func (source blockingSource) status(ctx context.Context, _ string) (any, error) {
	return source.list(ctx)
}

type channelWriter chan []byte

func (writer channelWriter) Write(data []byte) (int, error) {
	writer <- append([]byte{}, data...)
	return len(data), nil
}

func awaitReply(t *testing.T, output channelWriter) []byte {
	t.Helper()
	select {
	case reply := <-output:
		return reply
	case <-time.After(5 * time.Second):
		t.Fatal("server did not respond")
		return nil
	}
}

func TestCancellationSuppressesReplyAndKeepsChannelUsable(t *testing.T) {
	input, client := io.Pipe()
	defer client.Close()
	output := make(channelWriter, 8)
	source := blockingSource{started: make(chan struct{}), release: make(chan struct{})}
	finished := make(chan error, 1)
	go func() { finished <- serve(t.Context(), input, output, source, "fixture") }()
	io.WriteString(client, testRequest("tools/call", `"name":"fitr_roles_list"`)+"\n")
	select {
	case <-source.started:
	case <-time.After(5 * time.Second):
		t.Fatal("tool did not start")
	}
	io.WriteString(client, `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1,"reason":"private secret"}}`+"\n")
	discover := strings.Replace(testRequest("server/discover", ""), `"id":1`, `"id":2`, 1)
	io.WriteString(client, discover+"\n")
	reply := awaitReply(t, output)
	if !bytes.Contains(reply, []byte(`"id":2`)) || bytes.Contains(reply, []byte("private")) {
		t.Fatalf("unexpected reply %s", reply)
	}
	client.Close()
	select {
	case err := <-finished:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("server did not stop after EOF")
	}
	if len(output) != 0 {
		t.Fatalf("cancelled request emitted a reply: %s", <-output)
	}
}

func TestContextCancellationClosesBlockedInput(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	input, client := io.Pipe()
	defer client.Close()
	finished := make(chan error, 1)
	go func() { finished <- serve(ctx, input, io.Discard, fixtureSource{}, "fixture") }()
	cancel()
	select {
	case err := <-finished:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("err=%v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancel did not stop server")
	}
}

func TestRateAndConcurrencyLimits(t *testing.T) {
	s := server{window: time.Now(), calls: 60, active: map[string]pending{}}
	call := []byte(testRequest("tools/call", `"name":"fitr_roles_list"`))
	if reply := s.accept(t.Context(), call); reply == nil || reply.Error == nil || reply.Error.Code != -32603 {
		t.Fatal("rate cap did not reject")
	}
	if !s.allowCall(s.window.Add(time.Minute)) || s.calls != 1 {
		t.Fatal("rate window did not reset")
	}
	for _, key := range []string{"n:1", "n:2", "n:3", "n:4"} {
		s.active[key] = pending{}
	}
	if reply := s.accept(t.Context(), call); reply == nil || reply.Error == nil || reply.Error.Code != -32600 {
		t.Fatal("duplicate ID did not reject")
	}
	call = bytes.Replace(call, []byte(`"id":1`), []byte(`"id":5`), 1)
	if reply := s.accept(t.Context(), call); reply == nil || reply.Error == nil || reply.Error.Code != -32603 {
		t.Fatal("concurrency cap did not reject")
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, errors.New("secret output path") }

func TestTransportAndToolFailuresDoNotLeak(t *testing.T) {
	for _, input := range []string{testRequest("server/discover", ""), testRequest("tools/call", `"name":"fitr_roles_list"`)} {
		err := serve(t.Context(), io.NopCloser(strings.NewReader(input+"\n")), brokenWriter{}, fixtureSource{}, "fixture")
		if err == nil || strings.Contains(err.Error(), "secret") {
			t.Fatalf("err=%v", err)
		}
	}
	err := serve(t.Context(), io.NopCloser(strings.NewReader(strings.Repeat("x", maxMessageBytes+2))), io.Discard, fixtureSource{}, "fixture")
	if err == nil {
		t.Fatal("oversized input accepted")
	}
	for _, source := range []fixtureSource{{err: errors.New("secret /home/token")}, {result: make(chan int)}, {result: strings.Repeat("x", maxMessageBytes+1)}} {
		var output bytes.Buffer
		err := serve(t.Context(), io.NopCloser(strings.NewReader(testRequest("tools/call", `"name":"fitr_roles_list"`)+"\n")), &output, source, "fixture")
		if err != nil || strings.Contains(output.String(), "secret") || (!strings.Contains(output.String(), `"isError":true`) && !strings.Contains(output.String(), `"error":`)) {
			t.Fatalf("reply=%s err=%v", output.Bytes(), err)
		}
	}
}

func TestToolArgumentSchemaChecks(t *testing.T) {
	for _, extra := range []string{`"name":"fitr_roles_list","arguments":{"path":"secret"}`, `"name":"fitr_role_review"`, `"name":"fitr_role_review","arguments":{"role":"ok","extra":true}`} {
		var output bytes.Buffer
		if err := serve(t.Context(), io.NopCloser(strings.NewReader(testRequest("tools/call", extra)+"\n")), &output, fixtureSource{}, "fixture"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), `"isError":true`) || strings.Contains(output.String(), "secret") {
			t.Fatal(output.String())
		}
	}
}

func TestMalformedAndDifferentlyTypedCancellationCannotCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	s := server{active: map[string]pending{"n:1": {ctx: ctx, cancel: cancel}}}
	for _, params := range []string{`{"requestId":"1"}`, `{"requestId":null}`, `{"requestId":1,"reason":[]}`, `{"requestId":1,"reason":null}`, `{"requestId":1,"unknown":true}`} {
		fields, ok := decodeObject([]byte(params))
		if !ok {
			t.Fatal(params)
		}
		s.notification(request{method: "notifications/cancelled", params: fields})
		if ctx.Err() != nil {
			t.Fatalf("malformed cancellation applied: %s", params)
		}
	}
	fields, _ := decodeObject([]byte(`{"requestId":1}`))
	s.notification(request{method: "notifications/cancelled", params: fields})
	if ctx.Err() == nil {
		t.Fatal("valid cancellation ignored")
	}
}

// A cancelled server must never report a clean stop. When the input closes at
// the same moment the context is cancelled, the loop's select is ready on both
// and chooses between them at random, so the answer cannot depend on which one
// it takes.
//
// Catching a regression means letting the reader goroutine close the input
// before the loop reaches its first select, which needs real scheduling
// pressure: an unloaded machine wins that race every time and a handful of
// sequential attempts prove nothing. At this width the unfixed code fails on
// every invocation, while the fixed code is ordering-independent by
// construction. CI reached the same state through the race detector instead.
func TestCancelledServeNeverReportsACleanStop(t *testing.T) {
	const attempts = 4096
	results := make(chan error, attempts)
	var wait sync.WaitGroup
	for range attempts {
		wait.Go(func() {
			ctx, cancel := context.WithCancel(t.Context())
			cancel()
			results <- serve(ctx, io.NopCloser(strings.NewReader("")), io.Discard, fixtureSource{}, "fixture")
		})
	}
	wait.Wait()
	close(results)
	for err := range results {
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled serve reported %v instead of an interruption", err)
		}
	}
}
