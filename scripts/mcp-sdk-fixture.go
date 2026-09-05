//go:build ignore

// This test helper synthesizes current-schema evidence from a historical fixture
// and seals it through the real record store. It measures nothing. Its output
// belongs only in the acceptance test's existing empty temporary directory.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/ollama"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/score"
)

func main() {
	if err := fixture(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func fixture() error {
	if len(os.Args) != 3 {
		return errors.New("usage: go run scripts/mcp-sdk-fixture.go <empty-results> <sealed-fixture>")
	}
	entries, err := os.ReadDir(os.Args[1])
	if err != nil || len(entries) != 0 {
		return errors.New("fixture destination must be an existing empty directory")
	}
	data, err := os.ReadFile(os.Args[2])
	if err != nil {
		return err
	}
	var result record.Record
	if err := json.Unmarshal(data, &result); err != nil {
		return err
	}
	if err := synthesize(&result); err != nil {
		return err
	}
	saved, err := (record.Store{Dir: os.Args[1]}).Save(&result)
	if err != nil {
		return err
	}
	return json.NewEncoder(os.Stdout).Encode(saved)
}

func synthesize(result *record.Record) error {
	const model, runtime, context = "private-model-canary", "synthetic-test-runtime", 8192
	profile, identity, previous := result.Completion.Profile, result.Manifest.Model, *result.Manifest.Provenance
	result.Manifest, result.Completion, result.RunID = nil, nil, ""
	result.SchemaVersion, result.Model = record.EvidenceSchemaVersion, model
	result.StartedAt = time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	result.NumCtx, result.Device.Runtime = context, runtime
	result.ModelMeta = ollama.ModelInfo{Name: model}
	if err := syntheticObservations(result, context); err != nil {
		return err
	}
	result.Scorecard = score.Score(result.Measured(), profile)
	provenance, err := record.NewRunProvenance(previous.TaskSetSHA256, previous.SpecSHA256, profile,
		record.CurrentScoringPolicy(), record.SoftwareReceipt{FitrVersion: previous.FitrVersion,
			SoftwareBuildSHA256: previous.SoftwareBuildSHA256, BackendProtocol: previous.BackendProtocol})
	if err != nil {
		return err
	}
	sum := sha256.Sum256([]byte(model))
	identity.Requested, identity.Resolved, identity.Runtime, identity.Value = model, model, runtime, "sha256:"+hex.EncodeToString(sum[:])
	if err := result.AttachManifest(identity, provenance); err != nil {
		return err
	}
	if err := result.CompleteEvidence(profile); err != nil {
		return err
	}
	if issue := result.EvidenceIntegrityIssue(); issue != "" {
		return fmt.Errorf("synthetic fixture integrity: %s", issue)
	}
	return nil
}

func syntheticObservations(result *record.Record, context int) error {
	result.Checks = nil
	for i := range 8 {
		result.Checks = append(result.Checks, eval.CheckOutcome{TaskID: fmt.Sprintf("check-%d", i),
			Family: fmt.Sprintf("family-%d", i), Need: "structured_output", Origin: "builtin", Seed: uint64(i),
			Pass: true, Outcome: eval.OutcomePass})
	}
	result.TaskPlan.CheckTrialsLimit = len(result.Checks)
	var err error
	result.TaskPlan.CheckPlanSHA256, err = record.ObservedCheckPlanSHA256(result.Checks)
	if err != nil {
		return err
	}
	fingerprint, err := device.NewFingerprintV2(result.Device, device.ContextVerification{
		RequestedTokens: context, EffectiveTokens: &context, EffectiveSource: device.ContextSourceRuntimeReport})
	if err != nil {
		return err
	}
	result.DeviceV2 = &fingerprint
	result.DeviceKey, err = fingerprint.ComparabilityKey()
	if err != nil {
		return err
	}
	result.TaskPlan.Memory = true
	result.Memory = eval.MemoryResult{Outcome: eval.OutcomePass, ResidentGB: 6, PctOnGPU: 100,
		RequestedCtx: context, EffectiveCtx: &context, ResidentBytes: 6 * device.GB, AcceleratorBytes: 6 * device.GB}
	result.EvidenceCounts, err = result.DeriveEvidenceCounts()
	return err
}
