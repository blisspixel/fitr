package main

import (
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

func TestRoleConfirmationNeverPullsMissingHFCandidate(t *testing.T) {
	for _, backend := range []string{"ollama", "auto"} {
		t.Run(backend, func(t *testing.T) {
			t.Setenv("FITR_RESULTS", t.TempDir())
			t.Setenv("FITR_BACKEND", "ollama")
			if backend == "ollama" {
				t.Setenv("FITR_BACKEND", "invalid-explicit-flag-must-win")
			}
			var reads, pulls, other atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == http.MethodGet && r.URL.Path == "/api/tags":
					reads.Add(1)
					_, _ = io.WriteString(w, `{"models":[]}`)
				case r.URL.Path == "/api/pull":
					pulls.Add(1)
					http.Error(w, "confirmation cannot download", http.StatusForbidden)
				default:
					other.Add(1)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			t.Setenv("OLLAMA_BASE_URL", server.URL)
			store, records := missingHFConfirmationLibrary(t)
			stderr, code := captureTopStderr(t, func() int {
				return startRoleConfirmation(t.Context(), store, records, roleConfirmationCommand{
					action: "confirm", mode: "none", backend: backend, args: []string{"coding"},
				})
			})
			if code != exitError || !strings.Contains(stderr, "did not resolve to an exact runtime entry") {
				t.Fatalf("missing identity returned %d: %s", code, stderr)
			}
			if reads.Load() != 2 || pulls.Load() != 0 || other.Load() != 0 {
				t.Fatalf("runtime calls: reads=%d pulls=%d other=%d", reads.Load(), pulls.Load(), other.Load())
			}
			life, err := store.LoadLifecycle("coding")
			if err != nil || len(life.Events) != 3 || life.Events[2].Action != "failed" || life.IncumbentSHA256 != "" {
				t.Fatalf("missing candidate did not terminate safely: %+v, %v", life, err)
			}
		})
	}
}

func missingHFConfirmationLibrary(t *testing.T) (role.Store, record.Store) {
	t.Helper()
	store := role.Store{Dir: filepath.Join(resultsDir(), ".roles")}
	records := record.Store{Dir: resultsDir()}
	spec := initialRoleSpec("coding", "structured_output", 0.5, 22, eval.NumCtx, 30)
	minimum := 1.0
	spec.Decision.Requirements = append(spec.Decision.Requirements, decision.Requirement{
		ID: "speed", Performance: &decision.PerformanceRequirement{Metric: decision.MetricDecodeTPS, AtLeast: &minimum},
	})
	spec.Preferences = []role.Preference{{Requirement: "speed", Weight: 1, Worst: 0, Best: 100}}
	library, err := store.Define(spec)
	if err != nil {
		t.Fatal(err)
	}
	started := time.Now().Add(-time.Hour)
	points := []*record.Record{
		cliRoleConfirmationRecord(t, "hf.co/acme/model:Q4_K_M", 80, started),
		cliRoleConfirmationRecord(t, "slow", 20, started),
	}
	cliRoleSaveSources(t, store, records, library, points)
	return store, records
}
