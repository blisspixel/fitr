package reference

import (
	"crypto/sha256"
	"encoding/hex"
	"strconv"
	"testing"
)

func fakeSHA(seed string) string {
	sum := sha256.Sum256([]byte(seed))
	return "sha256:" + hex.EncodeToString(sum[:])
}

func sealedPlan(t *testing.T, release, artifact string) Plan {
	t.Helper()
	draft := Plan{
		Dataset: DatasetID, DatasetRevision: DatasetRevision, DatasetSplit: DatasetSplit, SubsetID: SubsetID,
		Instances: withDigests(), HarnessName: "mini-swe-agent", HarnessVersion: "1.0.0",
		HarnessConfigSHA256: fakeSHA("config"), StepLimit: 20, TaskWallSeconds: 60, ContextCap: 8192, K: 1,
		SeedSet: "0123456789abcdef0123456789abcdef", WallBudgetSeconds: 3600,
		DiskBudgetBytes: 1 << 30, RequiredDiskBytes: 1 << 20,
		PrefillTokensPerSecond: 1000, DecodeTokensPerSecond: 100,
		PromptTokens: 100, CompletionTokens: 50,
		ArtifactSHA256: artifact, ArtifactRelease: release, Quant: "Q4_K_M",
		Runtime: "ollama", RuntimeBuild: "0.11.4", DeviceKey: fakeSHA("device"),
	}
	plan, err := NewPlan(draft)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func withDigests() []Instance {
	subset := DefaultSubset()
	out := make([]Instance, len(subset))
	for i, item := range subset {
		out[i] = Instance{ID: item.ID, Created: item.Created, ImageDigest: fakeSHA(item.ID)}
	}
	return out
}

func ready(plan Plan) RunConditions {
	return RunConditions{WorkerProbeAvailable: true, ServedContext: plan.ContextCap}
}

func rowsLike(plan Plan, verdict string) []Observation {
	rows := make([]Observation, 0, len(plan.Instances)*plan.K)
	for _, instance := range plan.Instances {
		for repeat := 1; repeat <= plan.K; repeat++ {
			rows = append(rows, Observation{
				InstanceID: instance.ID, Repeat: repeat, Verdict: verdict, Steps: 1,
				PromptTokens: 10, CompletionTokens: 5, WallMillis: 100,
				VerifierLogSHA256: fakeSHA(instance.ID + strconv.Itoa(repeat)),
			})
		}
	}
	return rows
}

func mustAnalyze(t *testing.T, plan Plan, rows []Observation) Result {
	t.Helper()
	result, err := Analyze(plan, ready(plan), rows)
	if err != nil {
		t.Fatal(err)
	}
	return result
}
