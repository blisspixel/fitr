package source

import (
	"crypto/tls"
	"net/http"
	"net/url"
)

// redirectTransport sends every request to a local test server while leaving
// the request URL untouched, so the code under test resolves real hosts.
type redirectTransport struct{ origin string }

func (t redirectTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	routed := r.Clone(r.Context())
	// Only the canonical provider host is rewritten to the test origin. A
	// redirect names a real test server and must reach it, or the harness
	// turns every redirect into a loop and hides the behaviour under test.
	if r.URL.Host == "huggingface.co" {
		target, err := url.Parse(t.origin)
		if err != nil {
			return nil, err
		}
		routed.URL.Scheme, routed.URL.Host = target.Scheme, target.Host
	}
	client := &http.Transport{TLSClientConfig: &tls.Config{InsecureSkipVerify: true}} //nolint:gosec // a local test server's self-signed certificate
	defer client.CloseIdleConnections()
	return client.RoundTrip(routed)
}
