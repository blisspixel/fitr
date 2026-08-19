package ollama

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestPullStreamsProgressAndSurfacesErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/pull" {
			t.Errorf("path = %s, want /api/pull", r.URL.Path)
		}
		flusher, _ := w.(http.Flusher)
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		flusher.Flush()
		fmt.Fprintln(w, `{"status":"downloading","total":100,"completed":40}`)
		flusher.Flush()
		fmt.Fprintln(w, `{"status":"success","total":100,"completed":100}`)
	}))
	defer srv.Close()

	c := &Client{BaseURL: srv.URL, HTTP: srv.Client()}
	var last string
	var lastPct int
	if err := c.Pull(context.Background(), "hf.co/org/model:Q4_K_M", func(status string, pct int) {
		last, lastPct = status, pct
	}); err != nil {
		t.Fatal(err)
	}
	if last != "success" || lastPct != 100 {
		t.Fatalf("last progress = %q %d%%, want success 100%%", last, lastPct)
	}

	errSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, `{"status":"pulling manifest"}`)
		fmt.Fprintln(w, `{"error":"file does not exist"}`)
	}))
	defer errSrv.Close()
	c = &Client{BaseURL: errSrv.URL, HTTP: errSrv.Client()}
	if err := c.Pull(context.Background(), "hf.co/missing/model", nil); err == nil {
		t.Fatal("a pull error from the server must surface")
	}
}
