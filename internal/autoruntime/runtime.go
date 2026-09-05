package autoruntime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/strictjson"
)

// Runtime owns exactly one job tree. It cannot stop a discovered/tray process.
type Runtime struct {
	mu                   sync.Mutex
	prepared             Prepared
	home, url, ownership string
	port                 int
	cmd                  *exec.Cmd
	guard                *processGuard
	done                 chan struct{}
	logs                 *servingLogs
	client               *http.Client
	closed               bool
	closeErr             error
	configurations       map[string]bool
	lease                *installationLease
}

var ErrOwnershipLost = errors.New("owned runtime process or listener ownership is unavailable")

// Start rechecks software before launch. The context owns the child's lifetime,
// not merely readiness. No generation request is made by this package.
func Start(ctx context.Context, p Prepared) (*Runtime, error) {
	if err := supported(); err != nil {
		return nil, err
	}
	if err := p.Spec.Validate(); err != nil {
		return nil, err
	}
	if p.seal == "" || p.seal != prepared(p.Spec).seal || p.ProfileSHA256 != prepared(p.Spec).ProfileSHA256 || p.LaunchConfigurationSHA256 != prepared(p.Spec).LaunchConfigurationSHA256 {
		return nil, errors.New("owned runtime preparation is absent or changed")
	}
	if err := validateModelStore(p.Spec.ModelStore); err != nil {
		return nil, err
	}
	lease, executable, libraries, err := inspectInstallation(ctx, p.Spec.Executable)
	if err != nil {
		return nil, err
	}
	if executable != p.Spec.ExecutableSHA256 || libraries != p.Spec.LibrariesSHA256 {
		_ = lease.close()
		return nil, errors.New("owned runtime changed after preparation")
	}
	version, err := inspectVersion(ctx, p.Spec.Executable)
	if err == nil && version != p.Spec.RuntimeVersion {
		err = errors.New("owned runtime version changed after preparation")
	}
	if err == nil {
		err = lease.verify(ctx)
	}
	if err != nil {
		_ = lease.close()
		return nil, err
	}
	r, err := startPrepared(ctx, p, lease)
	if err != nil {
		_ = lease.close()
	}
	return r, err
}

func startPrepared(ctx context.Context, p Prepared, lease *installationLease) (*Runtime, error) {
	r, listener, nonce, err := prepareRuntimeLaunch(ctx, p, lease)
	if err != nil {
		return nil, err
	}
	// Ollama cannot inherit a listening socket. Native PID checks below reject
	// an unrelated listener winning this short release/bind interval.
	if err = listener.Close(); err != nil {
		_ = removePrivateHome(r.home)
		return nil, err
	}
	if err = lease.verify(ctx); err != nil {
		_ = removePrivateHome(r.home)
		return nil, err
	}
	if err = validateModelStore(p.Spec.ModelStore); err != nil {
		_ = removePrivateHome(r.home)
		return nil, err
	}
	r.guard, err = startProcess(r.cmd)
	if err != nil {
		_ = removePrivateHome(r.home)
		return nil, err
	}
	r.ownership = sealJSON("fitr.autoruntime.ownership.v1", struct {
		Nonce     string
		PID, Port int
	}{
		hex.EncodeToString(nonce), r.cmd.Process.Pid, r.port})
	go func() { _ = r.cmd.Wait(); close(r.done) }()
	r.client = &http.Client{Transport: newOwnedTransport(r),
		CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("owned runtime redirects are refused") }}
	ready, cancel := context.WithTimeout(ctx, StartupTimeout)
	defer cancel()
	if err = r.awaitReady(ready); err != nil {
		return nil, errors.Join(err, r.Close())
	}
	go func() {
		select {
		case <-ctx.Done():
			_ = r.Close()
		case <-r.done:
			_ = r.Close()
		}
	}()
	return r, nil
}

func (r *Runtime) awaitReady(ctx context.Context) error {
	timer := time.NewTicker(50 * time.Millisecond)
	defer timer.Stop()
	for {
		cloudDisabled, logErr := r.logs.status()
		if logErr != nil {
			return logErr
		}
		if r.Alive() == nil && cloudDisabled {
			var version struct {
				Version string `json:"version"`
			}
			if data, err := r.readMetadata(ctx, http.MethodGet, "/api/version", nil, 4096); err == nil &&
				strictjson.Unmarshal(data, &version) == nil && version.Version == r.prepared.Spec.RuntimeVersion {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("owned Ollama did not establish PID-bound readiness, matching version and cloud-disabled startup: %w", ctx.Err())
		case <-r.done:
			return errors.New("owned Ollama exited before verified readiness")
		case <-timer.C:
		}
	}
}

func (r *Runtime) URL() string              { return r.url }
func (r *Runtime) HTTPClient() *http.Client { return r.client }

// Alive checks the original process handle and the current listener PID.
func (r *Runtime) Alive() (err error) {
	defer func() {
		if err != nil {
			err = errors.Join(ollama.ErrUnverifiedLocalExecution, fmt.Errorf("%w: %w", ErrOwnershipLost, err))
		}
	}()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return errors.New("owned runtime is closed")
	}
	select {
	case <-r.done:
		return errors.New("owned runtime has exited")
	default:
	}
	if err := r.guard.alive(); err != nil {
		return err
	}
	if _, err := r.logs.status(); err != nil {
		return err
	}
	return listenerOwned(r.cmd.Process.Pid, r.port)
}

// Close is idempotent and terminates the job tree, including runner children.
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed {
		return r.closeErr
	}
	r.closed = true
	stopErr := r.guard.stop()
	select {
	case <-r.done:
	case <-time.After(5 * time.Second):
		r.closeErr = errors.Join(stopErr, errors.New("owned runtime did not exit within cleanup deadline"))
		return r.closeErr
	}
	r.client.CloseIdleConnections()
	r.closeErr = errors.Join(stopErr, r.lease.close(), removePrivateHome(r.home))
	return r.closeErr
}

func (r *Runtime) BindingMetadata(configurationSHA256, artifactDigest string) (record.RuntimeBinding, error) {
	if err := r.Alive(); err != nil {
		return record.RuntimeBinding{}, err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.closed || !r.configurations[configurationSHA256] {
		return record.RuntimeBinding{}, errors.New("model configuration was not observed from this owned runtime")
	}
	b := record.RuntimeBinding{Schema: record.RuntimeBindingSchema, Kind: "owned_ollama",
		ProfileSHA256: r.prepared.ProfileSHA256, ExecutableSHA256: r.prepared.Spec.ExecutableSHA256,
		LaunchConfigurationSHA256: r.prepared.LaunchConfigurationSHA256, RuntimeVersion: r.prepared.Spec.RuntimeVersion,
		ModelConfigurationSHA256: configurationSHA256, ArtifactDigest: artifactDigest, OwnershipSHA256: r.ownership}
	return b, b.Validate()
}

type ownedTransport struct {
	runtime   *Runtime
	base      *http.Transport
	inference *http.Transport
}

func newOwnedTransport(runtime *Runtime) *ownedTransport {
	base := &http.Transport{Proxy: nil, DialContext: (&net.Dialer{Timeout: 3 * time.Second}).DialContext,
		ResponseHeaderTimeout: 5 * time.Second, MaxResponseHeaderBytes: 64 << 10}
	inference := base.Clone()
	// An inference response can wait for a cold model load before sending
	// headers. Metadata remains short-lived, and admission bounds the entire
	// inference with its original point/session deadline.
	inference.ResponseHeaderTimeout = 2 * time.Minute
	return &ownedTransport{runtime: runtime, base: base, inference: inference}
}

func (t *ownedTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Scheme != "http" || req.URL.Host != strings.TrimPrefix(t.runtime.url, "http://") || req.URL.User != nil ||
		req.URL.RawQuery != "" || req.URL.Fragment != "" || req.URL.Opaque != "" || req.URL.RawPath != "" {
		return nil, errors.New("request does not target the owned loopback runtime")
	}
	allowed := (req.Method == http.MethodGet && (req.URL.Path == "/api/version" || req.URL.Path == "/api/tags" || req.URL.Path == "/api/ps")) ||
		(req.Method == http.MethodPost && (req.URL.Path == "/api/show" || req.URL.Path == "/api/generate" || req.URL.Path == "/api/chat"))
	if !allowed {
		return nil, errors.New("owned runtime permits only bounded inspection and admitted inference endpoints")
	}
	for _, key := range []string{"Authorization", "Proxy-Authorization", "Cookie"} {
		if req.Header.Get(key) != "" {
			return nil, errors.New("owned runtime refuses credential headers")
		}
	}
	if err := t.runtime.Alive(); err != nil {
		return nil, err
	}
	transport := t.base
	if req.URL.Path == "/api/generate" || req.URL.Path == "/api/chat" {
		transport = t.inference
	}
	response, err := transport.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	if err := t.runtime.Alive(); err != nil {
		_ = response.Body.Close()
		return nil, err
	}
	response.Body = &ownedBody{ReadCloser: response.Body, runtime: t.runtime}
	return response, nil
}

func (t *ownedTransport) CloseIdleConnections() {
	t.base.CloseIdleConnections()
	t.inference.CloseIdleConnections()
}

type ownedBody struct {
	io.ReadCloser
	runtime *Runtime
}

func (b *ownedBody) Read(data []byte) (int, error) {
	if err := b.runtime.aliveDuringRead(); err != nil {
		return 0, err
	}
	n, err := b.ReadCloser.Read(data)
	if ownErr := b.runtime.aliveDuringRead(); ownErr != nil {
		return 0, ownErr
	}
	if err != nil {
		if ownErr := b.runtime.Alive(); ownErr != nil {
			return 0, ownErr
		}
	}
	return n, err
}

// Preserve process liveness on every read without repeatedly scanning the
// machine's TCP table or copying logs inside streaming latency measurements.
// Full listener ownership is checked at dispatch, headers and terminal read.
func (r *Runtime) aliveDuringRead() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	_, logErr := r.logs.status()
	if r.closed || logErr != nil {
		return errors.Join(ollama.ErrUnverifiedLocalExecution, ErrOwnershipLost)
	}
	if err := r.guard.alive(); err != nil {
		return errors.Join(ollama.ErrUnverifiedLocalExecution, ErrOwnershipLost, err)
	}
	return nil
}

func prepareRuntimeLaunch(ctx context.Context, p Prepared, lease *installationLease) (*Runtime, net.Listener, []byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, nil, err
	}
	home, err := privateHome()
	if err != nil {
		return nil, nil, nil, err
	}
	listener, err := (&net.ListenConfig{}).Listen(ctx, "tcp4", "127.0.0.1:0")
	if err != nil {
		_ = removePrivateHome(home)
		return nil, nil, nil, err
	}
	address := listener.Addr().String()
	_, portText, _ := net.SplitHostPort(address)
	port, _ := strconv.Atoi(portText)
	env, err := childEnvironment(p.Spec, home, address)
	if err != nil {
		_ = listener.Close()
		_ = removePrivateHome(home)
		return nil, nil, nil, err
	}
	nonce := make([]byte, 32)
	if _, err = rand.Read(nonce); err != nil {
		_ = listener.Close()
		_ = removePrivateHome(home)
		return nil, nil, nil, err
	}
	r := &Runtime{prepared: p, home: home, url: "http://" + address, port: port, logs: &servingLogs{},
		done: make(chan struct{}), configurations: make(map[string]bool), lease: lease}
	r.cmd = exec.CommandContext(ctx, p.Spec.Executable, "serve")
	r.cmd.Env, r.cmd.Dir = env, home
	r.cmd.Stdout, r.cmd.Stderr = r.logs.writer(0), r.logs.writer(1)
	return r, listener, nonce, nil
}
