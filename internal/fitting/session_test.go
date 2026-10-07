package fitting

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/blisspixel/fitr/internal/eval"
)

func TestStoreRejectsLinkedManagedDirectory(t *testing.T) {
	plan, err := Draft(ceilingRequest(t), testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	root, target := t.TempDir(), t.TempDir()
	if err := os.Symlink(target, filepath.Join(root, ".fitting")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	session := Session{Schema: SessionSchema, Plan: plan, Phase: PhasePreviewed}
	if err := (Store{Results: root}).Create(session); err == nil {
		t.Fatal("fitting wrote through a linked managed directory")
	}
	if _, err := os.Stat(filepath.Join(target, plan.ID+".json")); !os.IsNotExist(err) {
		t.Fatalf("linked target was mutated: %v", err)
	}
}

func TestSessionRoundTripSkipsCompletedPoints(t *testing.T) {
	plan, err := Draft(ceilingRequest(t), testTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store := Store{Results: root}
	session := Session{Schema: SessionSchema, Phase: PhaseMeasuring, Plan: plan, ExecutableSHA256: "sha256:binary"}
	session.Points = []Point{{Model: plan.Candidates[0], RunID: "run-00001", EvidenceSHA256: "sha256:" + strings.Repeat("c", 64)}}
	if err := store.Create(session); err != nil {
		t.Fatal(err)
	}
	loaded, err := store.Load(plan.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !loaded.Completed(plan.Candidates[0]) || loaded.ExecutableSHA256 != "sha256:binary" || loaded.Plan.SHA256 != plan.SHA256 {
		t.Fatalf("%+v", loaded)
	}
	action, err := loaded.ResumeAction()
	if err != nil || action != "measure" {
		t.Fatal(action, err)
	}
	if _, err := store.Load("../" + plan.ID); err == nil {
		t.Fatal("path traversal was accepted")
	}
	loaded.Plan.ContextTokens = 1
	if err := store.Save(loaded); err == nil {
		t.Fatal("tampered plan was saved")
	}
}

func FuzzDecodeSession(f *testing.F) {
	tasks, err := eval.LoadSpec()
	if err != nil {
		f.Fatal(err)
	}
	plan, err := Draft(Request{
		RoleName: "daily", Outcomes: []string{"structured_output"}, Scope: ScopeQualify,
		Candidates: []string{"model"}, ContextTokens: 8192,
		CapacityKind: CapacityCeiling, CapacityBytes: 1 << 30, ResidentLimitBytes: 1 << 30,
		Endpoint: "http://127.0.0.1:11434", EndpointSource: "explicit", Locality: "loopback-unproven",
		BuildVersion: "test", Now: time.Unix(1, 0),
	}, tasks)
	if err != nil {
		f.Fatal(err)
	}
	data, err := json.Marshal(Session{Schema: SessionSchema, Phase: PhasePreviewed, Plan: plan})
	if err != nil {
		f.Fatal(err)
	}
	f.Add(data)
	f.Add([]byte(`{"schema":"fitr.fitting.session.v1","schema":"duplicate"}`))
	f.Add([]byte(`{} {}`))
	f.Fuzz(func(t *testing.T, raw []byte) {
		session, err := decodeSession(raw)
		if err != nil {
			return
		}
		if err := session.Validate(); err != nil {
			t.Fatalf("accepted invalid session: %v", err)
		}
	})
}
