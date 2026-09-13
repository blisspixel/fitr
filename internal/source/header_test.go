package source

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The bytes of a large artifact are not on the host that serves its metadata.
// A provider answers the canonical URL with a redirect to a signed location on
// a content network it chooses, so refusing every redirect refuses the bytes.
func TestPrefixFollowsOneRedirectAndRecordsTheHostThatServed(t *testing.T) {
	bytesServer := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Range") == "" {
			t.Error("prefix read did not ask for a byte range")
		}
		w.Header().Set("Content-Range", "bytes 0-14/15")
		w.WriteHeader(http.StatusPartialContent)
		w.Write([]byte("GGUF\x03\x00\x00\x00payload"))
	}))
	defer bytesServer.Close()
	origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, bytesServer.URL+"/signed", http.StatusFound)
	}))
	defer origin.Close()

	resolver := NewResolver(redirectTransport{origin: origin.URL})
	body, observation := resolver.FetchArtifactPrefix(context.Background(), "o/r", sourceCommit, "model.gguf")
	if observation.Outcome != "resolved" {
		t.Fatalf("outcome = %q (%+v)", observation.Outcome, observation)
	}
	if !strings.HasPrefix(string(body), "GGUF") {
		t.Fatalf("body = %q", string(body))
	}
	if observation.Redirects != 1 {
		t.Fatalf("redirects = %d, want exactly one", observation.Redirects)
	}
	// The host that served the bytes is not the host that was asked, and a
	// projection built from them has to be able to say so.
	if observation.Host == "" || observation.Host == observation.RequestedHost {
		t.Fatalf("serving host was not recorded distinctly: %+v", observation)
	}
}

// A chain is not followed. One hop reaches the provider's storage; more makes
// the host that answers progressively less predictable.
func TestPrefixRefusesARedirectChain(t *testing.T) {
	var server *httptest.Server
	server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, server.URL+"/again", http.StatusFound)
	}))
	defer server.Close()
	resolver := NewResolver(redirectTransport{origin: server.URL})
	body, observation := resolver.FetchArtifactPrefix(context.Background(), "o/r", sourceCommit, "model.gguf")
	if body != nil || observation.Outcome != "redirect_limit" {
		t.Fatalf("a redirect chain was followed: %+v", observation)
	}
}

// A redirect away from https, or to a relative location, is not followed.
func TestPrefixRefusesAnInsecureRedirect(t *testing.T) {
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Location", "http://example.invalid/plain")
		w.WriteHeader(http.StatusFound)
	}))
	defer server.Close()
	resolver := NewResolver(redirectTransport{origin: server.URL})
	body, observation := resolver.FetchArtifactPrefix(context.Background(), "o/r", sourceCommit, "model.gguf")
	if body != nil || observation.Outcome != "redirect_rejected" {
		t.Fatalf("an insecure redirect was followed: %+v", observation)
	}
}

func TestPrefixRequiresAPinnedCommitAndFile(t *testing.T) {
	resolver := NewResolver(redirectTransport{origin: "https://example.invalid"})
	for _, test := range []struct{ repo, commit, file string }{
		{"", "c", "f"}, {"o/r", "", "f"}, {"o/r", "c", ""},
	} {
		_, observation := resolver.FetchArtifactPrefix(context.Background(), test.repo, test.commit, test.file)
		if observation.Outcome != "request_invalid" {
			t.Fatalf("accepted %+v", test)
		}
	}
}
