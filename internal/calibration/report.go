// Package calibration builds privacy-safe evidence from paired check runs.
package calibration

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"
	"time"
	"unicode"

	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const (
	LegacyPairSchema              = "fitr.calibration.pair.v1"
	PairSchema                    = "fitr.calibration.pair.v2"
	SummarySchema                 = "fitr.calibration.summary.v2"
	TrustReceiptSchema            = "fitr.calibration.trust.ed25519.v1"
	DecisionGradeMinInstances     = 10
	DecisionGradeMinDevices       = 2
	DecisionGradeMinModelFamilies = 2

	LineageSchema     = "fitr.lineage.same-base.v1"
	ConversionSchema  = "fitr.lineage.conversion.v1"
	LineageConversion = "publisher_conversion_manifest"
	LineageGGUFDigest = "gguf_base_digest"

	maxCalibrationJSONBytes = 16 << 20
)

// Device contains the measurement-relevant hardware fields but deliberately
// omits the hostname. DeviceID lets aggregation deduplicate a box without
// publishing that hostname.
type Device struct {
	ID              string            `json:"id"`
	OS              string            `json:"os"`
	CPU             string            `json:"cpu"`
	RAMGB           float64           `json:"ram_gb"`
	GPU             string            `json:"gpu"`
	GPUDriver       string            `json:"gpu_driver"`
	GPUDriverDate   string            `json:"gpu_driver_date,omitempty"`
	GPUBackend      string            `json:"gpu_backend,omitempty"`
	Runtime         string            `json:"runtime"`
	InferenceDevice string            `json:"inference_device"`
	Config          map[string]string `json:"config,omitempty"`
}

// PseudonymousDeviceID hashes the full local comparison key. The report can
// recognize repeat submissions from one box without carrying the hostname.
// The stable value can link reports from that box, so it is pseudonymous rather
// than anonymous.
func PseudonymousDeviceID(deviceKey string) string {
	return pseudonymousID("fitr-calibration-v1", deviceKey)
}

// PseudonymousSeedSetID keeps independently submitted pairs linkable by their
// controlled instances without exporting a user-chosen seedset name. Seedset
// names can accidentally contain hostnames, project names, or other local
// identifiers.
func PseudonymousSeedSetID(seedSet string) string {
	if strings.TrimSpace(seedSet) == "" {
		return ""
	}
	return pseudonymousID("fitr-calibration-seedset-v1", seedSet)
}

func pseudonymousID(domain, value string) string {
	sum := sha256.Sum256([]byte(domain + "\x00" + value))
	return hex.EncodeToString(sum[:8])
}

// Run identifies one side of a calibration pair. It contains no prompts or
// model output.
type Run struct {
	Model               string `json:"model"`
	Quant               string `json:"quant,omitempty"`
	Family              string `json:"family,omitempty"`
	ParameterSize       string `json:"parameter_size,omitempty"`
	StartedAt           string `json:"started_at"`
	NumCtx              int    `json:"num_ctx"`
	ResultSchemaVersion int    `json:"result_schema_version"`
	// ArtifactDigest is the runtime-bound SHA-256 of the served artifact.
	// Observed-only local file hashes are omitted: they do not prove which
	// bytes the process loaded and cannot support lineage.
	ArtifactDigest string `json:"artifact_digest,omitempty"`
}

// Item is the paired pass/fail agreement for one generated task family.
type Item struct {
	TaskID          string `json:"task"`
	Family          string `json:"family"`
	Need            string `json:"need"`
	Shared          int    `json:"shared"`
	Flips           int    `json:"flips"`
	ReferencePasses int    `json:"reference_passes"`
	CandidatePasses int    `json:"candidate_passes"`
}

// PairReport is the shareable evidence from two paired runs. Raw prompts,
// model responses, result paths, and hostnames are intentionally absent.
type PairReport struct {
	Schema      string `json:"schema"`
	CreatedAt   string `json:"created_at"`
	FitrVersion string `json:"fitr_version"`
	SpecVersion int    `json:"spec_version"`
	SeedSet     string `json:"seedset"`
	Device      Device `json:"device"`

	Reference Run `json:"reference"`
	Candidate Run `json:"candidate"`

	Shared        int             `json:"shared"`
	Flips         int             `json:"flips"`
	Discriminated int             `json:"items_discriminated"`
	NeverObserved int             `json:"items_never_observed"`
	Direction     string          `json:"direction"`
	Items         []Item          `json:"items"`
	Lineage       *LineageReceipt `json:"lineage,omitempty"`
	Trust         *TrustReceipt   `json:"trust,omitempty"`
}

// TrustReceipt proves that the report has not changed since its issuer signed
// it. It establishes evidence integrity, not the real-world identity of the
// issuer. Unsigned legacy and imported reports remain exploratory.
type TrustReceipt struct {
	Schema        string `json:"schema"`
	PublicKey     string `json:"public_key"`
	PayloadSHA256 string `json:"payload_sha256"`
	Signature     string `json:"signature"`
}

// TrustPolicy is an external allowlist of public keys. Report-carried keys are
// self-asserted and provide tamper evidence only; they cannot authenticate
// device or model-family claims without this separate trust root.
type TrustPolicy struct {
	PublicKeys []string
}

// NewTrustPolicy builds an explicit trust root from independently obtained
// Ed25519 public keys.
func NewTrustPolicy(keys ...ed25519.PublicKey) TrustPolicy {
	p := TrustPolicy{}
	for _, key := range keys {
		if len(key) == ed25519.PublicKeySize {
			p.PublicKeys = append(p.PublicKeys, base64.RawStdEncoding.EncodeToString(key))
		}
	}
	sort.Strings(p.PublicKeys)
	return p
}

func (p TrustPolicy) verify(r PairReport) error {
	if err := VerifyPairTrust(r); err != nil {
		return err
	}
	for _, trusted := range p.PublicKeys {
		if trusted == r.Trust.PublicKey {
			return nil
		}
	}
	return errors.New("calibration signer is not in the external trust policy")
}

// PairAssessment evaluates a valid pair against the published
// decision-grade evidence controls. Exploratory reports remain valid inputs to
// Aggregate, but they do not count toward campaign readiness.
type PairAssessment struct {
	DecisionGrade            bool     `json:"decision_grade"`
	TrustedEvidence          bool     `json:"trusted_evidence"`
	SameBaseLineageVerified  bool     `json:"same_base_lineage_verified"`
	HigherPrecisionReference bool     `json:"higher_precision_reference"`
	FixedInstances           bool     `json:"fixed_instances"`
	MinimumInstancesPerTask  int      `json:"minimum_instances_per_task"`
	MaximumInstancesPerTask  int      `json:"maximum_instances_per_task"`
	ReferenceHealthy         bool     `json:"reference_healthy"`
	CandidateDamaged         bool     `json:"candidate_damaged"`
	Reasons                  []string `json:"reasons,omitempty"`
}

// AssessPair checks the controls that make a pair decision-grade. It does not
// decide whether any task should change.
func AssessPair(r PairReport) PairAssessment {
	return AssessPairWithTrust(r, TrustPolicy{})
}

// AssessPairWithTrust applies decision-grade controls using an explicit trust
// root. The default AssessPair deliberately supplies no trusted issuers.
func AssessPairWithTrust(r PairReport, trust TrustPolicy) PairAssessment {
	rankedQuantReference := (r.Direction == "dtype_rank_reference" || r.Direction == "higher_precision_reference") &&
		eval.QuantRank(r.Reference.Quant) > eval.QuantRank(r.Candidate.Quant) &&
		eval.QuantRank(r.Candidate.Quant) > 0
	a := PairAssessment{
		TrustedEvidence:  trust.verify(r) == nil,
		FixedInstances:   len(r.Items) > 0,
		ReferenceHealthy: len(r.Items) > 0,
	}
	if !a.TrustedEvidence {
		a.Reasons = append(a.Reasons, "report has no verifiable trust receipt")
	}
	// A trusted report signature seals the claims present in the report. It
	// cannot manufacture missing lineage evidence. Same-base verification
	// requires a structurally valid lineage receipt that independently binds
	// both runtime-bound artifact digests to one base revision.
	a.SameBaseLineageVerified = r.Lineage != nil &&
		r.Lineage.Bind(r.Reference.ArtifactDigest, r.Candidate.ArtifactDigest) == nil
	if !a.SameBaseLineageVerified {
		a.Reasons = append(a.Reasons, "same-base model revision lineage is not verified")
	}
	for _, item := range r.Items {
		if a.MinimumInstancesPerTask == 0 || item.Shared < a.MinimumInstancesPerTask {
			a.MinimumInstancesPerTask = item.Shared
		}
		if item.Shared > a.MaximumInstancesPerTask {
			a.MaximumInstancesPerTask = item.Shared
		}
		if item.ReferencePasses != item.Shared {
			a.ReferenceHealthy = false
		}
		// Preserve item-level pass counts in the report, but do not convert a
		// difference into a causal damage flag without same-base lineage.
	}
	a.HigherPrecisionReference = a.SameBaseLineageVerified && rankedQuantReference
	if a.SameBaseLineageVerified {
		for _, item := range r.Items {
			if item.CandidatePasses < item.ReferencePasses {
				a.CandidateDamaged = true
			}
		}
	}
	a.FixedInstances = a.FixedInstances && a.MinimumInstancesPerTask == a.MaximumInstancesPerTask
	if a.SameBaseLineageVerified && !a.HigherPrecisionReference {
		a.Reasons = append(a.Reasons, "higher-precision reference is not verified")
	}
	if !a.FixedInstances {
		a.Reasons = append(a.Reasons, "tasks do not share one fixed instance count")
	}
	if a.MinimumInstancesPerTask < DecisionGradeMinInstances {
		a.Reasons = append(a.Reasons, fmt.Sprintf("fewer than %d instances per task", DecisionGradeMinInstances))
	}
	if !a.ReferenceHealthy {
		a.Reasons = append(a.Reasons, "higher-precision reference is not healthy across the battery")
	}
	if a.SameBaseLineageVerified && !a.CandidateDamaged {
		a.Reasons = append(a.Reasons, "lower-precision candidate shows no damage")
	}
	a.DecisionGrade = len(a.Reasons) == 0
	return a
}

// NewPair builds a report and places the higher ranked dtype in Reference when
// the dtypes have a known ordering. This is display ordering, not evidence that
// the artifacts share a base-model revision. Unknown schemes retain input order.
func NewPair(fitrVersion string, specVersion int, seedSet string, device Device,
	a, b Run, stats []eval.ItemStat) PairReport {
	stats = append([]eval.ItemStat(nil), stats...)
	a.Model = shareableModelID(a.Model)
	b.Model = shareableModelID(b.Model)
	device.OS = strings.TrimSpace(device.OS)
	device.CPU = strings.Join(strings.Fields(device.CPU), " ")
	device.GPU = strings.Join(strings.Fields(device.GPU), " ")
	device.RAMGB = math.Round(device.RAMGB*10) / 10
	device.Config = shareableConfig(device.Config)
	direction := "input_order"
	ra, rb := eval.QuantRank(a.Quant), eval.QuantRank(b.Quant)
	if ra > 0 && rb > 0 && ra != rb {
		direction = "dtype_rank_reference"
		if rb > ra {
			a, b = b, a
			for i := range stats {
				stats[i].APass, stats[i].BPass = stats[i].BPass, stats[i].APass
			}
		}
	}

	r := PairReport{
		Schema: PairSchema, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		FitrVersion: fitrVersion, SpecVersion: specVersion, SeedSet: PseudonymousSeedSetID(seedSet),
		Device: device, Reference: a, Candidate: b, Direction: direction,
	}
	for _, s := range stats {
		r.Shared += s.Shared
		r.Flips += s.Flips
		if s.Discriminated() {
			r.Discriminated++
		} else {
			r.NeverObserved++
		}
		r.Items = append(r.Items, Item{
			TaskID: s.TaskID, Family: s.Family, Need: s.Need,
			Shared: s.Shared, Flips: s.Flips,
			ReferencePasses: s.APass, CandidatePasses: s.BPass,
		})
	}
	sort.Slice(r.Items, func(i, j int) bool {
		if r.Items[i].Flips != r.Items[j].Flips {
			return r.Items[i].Flips > r.Items[j].Flips
		}
		return r.Items[i].TaskID < r.Items[j].TaskID
	})
	return normalizePair(r)
}

func shareableConfig(config map[string]string) map[string]string {
	allowed := map[string]bool{
		"OLLAMA_IGPU_ENABLE":       true,
		"OLLAMA_FLASH_ATTENTION":   true,
		"OLLAMA_KV_CACHE_TYPE":     true,
		"OLLAMA_MAX_LOADED_MODELS": true,
		"OLLAMA_NUM_PARALLEL":      true,
		"OLLAMA_CONTEXT_LENGTH":    true,
		"LLAMA_ARG_FIT":            true,
	}
	out := map[string]string{}
	for key, value := range config {
		value = shareableText(value)
		if allowed[key] && value != "" {
			if key == "LLAMA_ARG_FIT" {
				out[key] = "configured"
			} else {
				out[key] = value
			}
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func shareableModelID(model string) string {
	model = shareableText(model)
	lower := strings.ToLower(model)
	local := strings.HasPrefix(model, "/") || strings.HasPrefix(model, `\\`) ||
		strings.HasPrefix(model, "./") || strings.HasPrefix(model, "../") ||
		strings.HasPrefix(model, `.\`) || strings.HasPrefix(model, `..\`) ||
		(len(model) >= 3 && model[1] == ':' && (model[2] == '\\' || model[2] == '/')) ||
		strings.Contains(model, "://") || strings.HasSuffix(lower, ".gguf")
	if local {
		return "local-" + pseudonymousID("fitr-calibration-model-v1", model)
	}
	return model
}

func shareableText(value string) string {
	value = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.C, r) {
			return ' '
		}
		return r
	}, value)
	return strings.Join(strings.Fields(value), " ")
}

// WriteJSON writes an indented report or summary.
func WriteJSON(path string, value any) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("missing output path")
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	if len(b) > maxCalibrationJSONBytes {
		return fmt.Errorf("calibration output exceeds %d bytes", maxCalibrationJSONBytes)
	}
	return atomicfile.Write(path, b, 0o644)
}

// ReadPair loads one pair report and rejects unrelated JSON.
func ReadPair(path string) (PairReport, error) {
	b, err := boundedio.ReadFile(path, maxCalibrationJSONBytes)
	if err != nil {
		return PairReport{}, err
	}
	if err := strictjson.Validate(b); err != nil {
		return PairReport{}, err
	}
	var r PairReport
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		return PairReport{}, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return PairReport{}, errors.New("content after the calibration report")
		}
		return PairReport{}, err
	}
	r = normalizePair(r)
	if err := validatePair(r); err != nil {
		return PairReport{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

// NewTrustKey creates a signing key for calibration tooling. The private key
// is never placed in a report.
func NewTrustKey() (ed25519.PrivateKey, error) {
	_, privateKey, err := ed25519.GenerateKey(rand.Reader)
	return privateKey, err
}

// SignPair normalizes and validates a report, upgrades it to the current
// schema, and attaches a signature over every field except Trust itself.
func SignPair(r *PairReport, privateKey ed25519.PrivateKey) error {
	if r == nil {
		return errors.New("cannot sign a nil calibration report")
	}
	if len(privateKey) != ed25519.PrivateKeySize {
		return errors.New("invalid calibration signing key")
	}
	r.Schema = PairSchema
	r.Trust = nil
	*r = normalizePair(*r)
	if err := validatePair(*r); err != nil {
		return err
	}
	payload, err := trustPayload(*r)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(append([]byte("fitr.calibration.trust.v1\x00"), payload...))
	publicKey, ok := privateKey.Public().(ed25519.PublicKey)
	if !ok {
		return errors.New("signing key does not carry an ed25519 public key")
	}
	r.Trust = &TrustReceipt{
		Schema:        TrustReceiptSchema,
		PublicKey:     base64.RawStdEncoding.EncodeToString(publicKey),
		PayloadSHA256: "sha256:" + hex.EncodeToString(sum[:]),
		Signature:     base64.RawStdEncoding.EncodeToString(ed25519.Sign(privateKey, payload)),
	}
	return nil
}

// VerifyPairTrust verifies the receipt and its complete normalized payload.
func VerifyPairTrust(r PairReport) error {
	if r.Trust == nil {
		return errors.New("calibration report is unsigned")
	}
	receipt := *r.Trust
	if receipt.Schema != TrustReceiptSchema {
		return fmt.Errorf("unsupported calibration trust schema %q", receipt.Schema)
	}
	r.Trust = nil
	payload, err := trustPayload(normalizePair(r))
	if err != nil {
		return err
	}
	sum := sha256.Sum256(append([]byte("fitr.calibration.trust.v1\x00"), payload...))
	want := "sha256:" + hex.EncodeToString(sum[:])
	if !strings.EqualFold(receipt.PayloadSHA256, want) {
		return errors.New("calibration trust digest does not match the report")
	}
	publicKey, err := base64.RawStdEncoding.DecodeString(receipt.PublicKey)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("calibration trust public key is invalid")
	}
	signature, err := base64.RawStdEncoding.DecodeString(receipt.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize || !ed25519.Verify(publicKey, payload, signature) {
		return errors.New("calibration trust signature is invalid")
	}
	return nil
}

func trustPayload(r PairReport) ([]byte, error) {
	r.Trust = nil
	return json.Marshal(r)
}

func normalizePair(r PairReport) PairReport {
	r.Schema = shareableText(r.Schema)
	r.CreatedAt = shareableText(r.CreatedAt)
	r.FitrVersion = shareableText(r.FitrVersion)
	r.Direction = shareableText(r.Direction)
	r.SeedSet = shareableText(r.SeedSet)
	if r.SeedSet == "" {
		r.SeedSet = ""
	} else if len(r.SeedSet) != 16 {
		r.SeedSet = PseudonymousSeedSetID(r.SeedSet)
	} else if _, err := hex.DecodeString(r.SeedSet); err != nil {
		r.SeedSet = PseudonymousSeedSetID(r.SeedSet)
	} else {
		r.SeedSet = strings.ToLower(r.SeedSet)
	}
	r.Reference.Model = shareableModelID(r.Reference.Model)
	r.Candidate.Model = shareableModelID(r.Candidate.Model)
	for _, run := range []*Run{&r.Reference, &r.Candidate} {
		run.Quant = shareableText(run.Quant)
		run.Family = shareableText(run.Family)
		run.ParameterSize = shareableText(run.ParameterSize)
		run.StartedAt = shareableText(run.StartedAt)
		if digest, err := normalizeDigest(run.ArtifactDigest); err == nil {
			run.ArtifactDigest = digest
		} else if strings.TrimSpace(run.ArtifactDigest) == "" {
			run.ArtifactDigest = ""
		}
	}
	if r.Lineage != nil {
		_ = r.Lineage.normalize()
	}
	r.Device.ID = strings.ToLower(shareableText(r.Device.ID))
	r.Device.OS = shareableText(r.Device.OS)
	r.Device.CPU = shareableText(r.Device.CPU)
	r.Device.GPU = shareableText(r.Device.GPU)
	r.Device.GPUDriver = shareableText(r.Device.GPUDriver)
	r.Device.GPUDriverDate = shareableText(r.Device.GPUDriverDate)
	r.Device.GPUBackend = shareableText(r.Device.GPUBackend)
	r.Device.Runtime = shareableText(r.Device.Runtime)
	r.Device.InferenceDevice = shareableText(r.Device.InferenceDevice)
	r.Device.Config = shareableConfig(r.Device.Config)
	for i := range r.Items {
		r.Items[i].TaskID = shareableText(r.Items[i].TaskID)
		r.Items[i].Family = shareableText(r.Items[i].Family)
		r.Items[i].Need = shareableText(r.Items[i].Need)
	}
	return r
}

func validatePair(r PairReport) error {
	if r.Schema != PairSchema && r.Schema != LegacyPairSchema {
		return fmt.Errorf("unsupported calibration pair schema %q", r.Schema)
	}
	if r.Device.ID == "" || r.SeedSet == "" || r.SpecVersion < 1 || len(r.Items) == 0 {
		return errors.New("incomplete calibration report")
	}
	if len(r.Device.ID) != 16 {
		return errors.New("device id is not a fitr pseudonymous identifier")
	}
	if _, err := hex.DecodeString(r.Device.ID); err != nil {
		return errors.New("device id is not a fitr pseudonymous identifier")
	}
	if r.Reference.Model == "" || r.Candidate.Model == "" ||
		r.Reference.Family == "" || !strings.EqualFold(r.Reference.Family, r.Candidate.Family) ||
		r.Reference.ParameterSize == "" || !strings.EqualFold(r.Reference.ParameterSize, r.Candidate.ParameterSize) {
		return errors.New("report is not a same-family, same-size model pair")
	}
	if r.Reference.ResultSchemaVersion < 1 || r.Reference.ResultSchemaVersion != r.Candidate.ResultSchemaVersion {
		return errors.New("result schema is missing or differs across the pair")
	}
	seen := map[string]bool{}
	shared, flips, discriminated := 0, 0, 0
	for _, item := range r.Items {
		if item.TaskID == "" || seen[item.TaskID] {
			return fmt.Errorf("missing or duplicate task id %q", item.TaskID)
		}
		if item.Shared < 1 || item.Flips < 0 || item.Flips > item.Shared ||
			item.ReferencePasses < 0 || item.ReferencePasses > item.Shared ||
			item.CandidatePasses < 0 || item.CandidatePasses > item.Shared {
			return fmt.Errorf("invalid counts for task %q", item.TaskID)
		}
		delta := item.ReferencePasses - item.CandidatePasses
		if delta < 0 {
			delta = -delta
		}
		maxFlips := item.ReferencePasses + item.CandidatePasses
		if other := 2*item.Shared - item.ReferencePasses - item.CandidatePasses; other < maxFlips {
			maxFlips = other
		}
		if item.Flips < delta || item.Flips > maxFlips || (item.Flips-delta)%2 != 0 {
			return fmt.Errorf("paired flips disagree with pass counts for task %q", item.TaskID)
		}
		seen[item.TaskID] = true
		shared += item.Shared
		flips += item.Flips
		if item.Flips > 0 {
			discriminated++
		}
	}
	if r.Shared != shared || r.Flips != flips || r.Discriminated != discriminated ||
		r.NeverObserved != len(r.Items)-discriminated {
		return errors.New("pair totals do not match item outcomes")
	}
	for _, run := range []Run{r.Reference, r.Candidate} {
		if strings.TrimSpace(run.ArtifactDigest) == "" {
			continue
		}
		if _, err := normalizeDigest(run.ArtifactDigest); err != nil {
			return fmt.Errorf("run artifact digest: %w", err)
		}
	}
	if r.Lineage != nil {
		if strings.TrimSpace(r.Reference.ArtifactDigest) == "" || strings.TrimSpace(r.Candidate.ArtifactDigest) == "" {
			return errors.New("lineage receipt requires runtime-bound artifact digests on both runs")
		}
		if err := r.Lineage.Bind(r.Reference.ArtifactDigest, r.Candidate.ArtifactDigest); err != nil {
			return err
		}
	}
	return nil
}

// SummaryItem combines the evidence for one task without deciding whether to
// keep or drop it. That decision needs coverage from multiple boxes and pairs.
type SummaryItem struct {
	TaskID               string `json:"task"`
	Family               string `json:"family"`
	Need                 string `json:"need"`
	Reports              int    `json:"reports"`
	Devices              int    `json:"devices"`
	Shared               int    `json:"shared"`
	Flips                int    `json:"flips"`
	DiscriminatedReports int    `json:"discriminated_reports"`
	DiscriminatedDevices int    `json:"discriminated_devices"`
	Status               string `json:"status"`
	DecisionGradeReports int    `json:"decision_grade_reports"`
	DecisionGradeDevices int    `json:"decision_grade_devices"`
	DecisionGradeShared  int    `json:"decision_grade_shared"`
	DecisionGradeFlips   int    `json:"decision_grade_flips"`
	DecisionGradeStatus  string `json:"decision_grade_status"`
	ReviewCandidate      bool   `json:"review_candidate"`
}

// CampaignReadiness reports whether the evidence has reached the documented
// device and model-family coverage needed to review never-flipped tasks.
type CampaignReadiness struct {
	ReadyForReview          bool     `json:"ready_for_review"`
	DecisionGradeReports    int      `json:"decision_grade_reports"`
	Devices                 int      `json:"devices"`
	ModelFamilies           int      `json:"model_families"`
	MinimumDevices          int      `json:"minimum_devices"`
	MinimumModelFamilies    int      `json:"minimum_model_families"`
	MinimumInstancesPerTask int      `json:"minimum_instances_per_task"`
	Missing                 []string `json:"missing,omitempty"`
}

// Summary aggregates independent pair reports. It labels observed evidence
// but never automates deletion from the task battery.
type Summary struct {
	Schema      string            `json:"schema"`
	CreatedAt   string            `json:"created_at"`
	SpecVersion int               `json:"spec_version"`
	Reports     int               `json:"reports"`
	Devices     int               `json:"devices"`
	ModelPairs  int               `json:"model_pairs"`
	Readiness   CampaignReadiness `json:"campaign_readiness"`
	Items       []SummaryItem     `json:"items"`
}

// Aggregate combines reports that used the same task specification. Exact
// duplicate submissions are rejected so one run cannot silently gain weight.
func Aggregate(reports []PairReport) (Summary, error) {
	return AggregateWithTrust(reports, TrustPolicy{})
}

// AggregateWithTrust permits readiness only for reports authenticated by a
// caller-supplied external trust root.
func AggregateWithTrust(reports []PairReport, trust TrustPolicy) (Summary, error) {
	if len(reports) == 0 {
		return Summary{}, errors.New("no calibration reports")
	}
	specVersion := reports[0].SpecVersion
	type itemAcc struct {
		family, need                                   string
		reports, shared, flips, discriminatedReports   int
		decisionReports, decisionShared, decisionFlips int
		devices, discriminatedDevices, decisionDevices map[string]bool
	}
	byItem := map[string]*itemAcc{}
	devices := map[string]bool{}
	pairs := map[string]bool{}
	decisionDevices := map[string]bool{}
	decisionFamilies := map[string]bool{}
	decisionReports := 0
	seen := map[string]bool{}
	var expectedItems map[string]bool
	for index, r := range reports {
		r = normalizePair(r)
		if err := validatePair(r); err != nil {
			return Summary{}, fmt.Errorf("report %d: %w", index+1, err)
		}
		if r.SpecVersion != specVersion {
			return Summary{}, fmt.Errorf("spec version mismatch: %d and %d", specVersion, r.SpecVersion)
		}
		key := strings.Join([]string{r.Device.ID, r.SeedSet, r.Reference.Model, r.Candidate.Model}, "\x00")
		if seen[key] {
			return Summary{}, fmt.Errorf("duplicate report for device %s, seedset %s, pair %s / %s",
				r.Device.ID, r.SeedSet, r.Reference.Model, r.Candidate.Model)
		}
		seen[key] = true
		devices[r.Device.ID] = true
		pairs[r.Reference.Model+"\x00"+r.Candidate.Model] = true
		decisionGrade := AssessPairWithTrust(r, trust).DecisionGrade
		if decisionGrade {
			decisionReports++
			decisionDevices[r.Device.ID] = true
			decisionFamilies[strings.ToLower(strings.TrimSpace(r.Reference.Family))] = true
		}
		itemSet := map[string]bool{}
		for _, item := range r.Items {
			if item.TaskID == "" || itemSet[item.TaskID] {
				return Summary{}, fmt.Errorf("report has missing or duplicate task id %q", item.TaskID)
			}
			itemSet[item.TaskID] = true
		}
		if expectedItems == nil {
			expectedItems = itemSet
		} else if !sameSet(expectedItems, itemSet) {
			return Summary{}, errors.New("calibration reports contain different task sets")
		}
		for _, item := range r.Items {
			a := byItem[item.TaskID]
			if a == nil {
				a = &itemAcc{family: item.Family, need: item.Need,
					devices: map[string]bool{}, discriminatedDevices: map[string]bool{},
					decisionDevices: map[string]bool{}}
				byItem[item.TaskID] = a
			} else if a.family != item.Family || a.need != item.Need {
				return Summary{}, fmt.Errorf("task %q changed family or need across reports", item.TaskID)
			}
			a.reports++
			a.shared += item.Shared
			a.flips += item.Flips
			a.devices[r.Device.ID] = true
			if item.Flips > 0 {
				a.discriminatedReports++
				a.discriminatedDevices[r.Device.ID] = true
			}
			if decisionGrade {
				a.decisionReports++
				a.decisionShared += item.Shared
				a.decisionFlips += item.Flips
				a.decisionDevices[r.Device.ID] = true
			}
		}
	}

	readiness := CampaignReadiness{
		DecisionGradeReports: decisionReports,
		Devices:              len(decisionDevices), ModelFamilies: len(decisionFamilies),
		MinimumDevices:          DecisionGradeMinDevices,
		MinimumModelFamilies:    DecisionGradeMinModelFamilies,
		MinimumInstancesPerTask: DecisionGradeMinInstances,
	}
	if readiness.Devices < readiness.MinimumDevices {
		readiness.Missing = append(readiness.Missing,
			fmt.Sprintf("need at least %d decision-grade physical devices", readiness.MinimumDevices))
	}
	if readiness.ModelFamilies < readiness.MinimumModelFamilies {
		readiness.Missing = append(readiness.Missing,
			fmt.Sprintf("need at least %d decision-grade model families", readiness.MinimumModelFamilies))
	}
	readiness.ReadyForReview = len(readiness.Missing) == 0
	s := Summary{
		Schema: SummarySchema, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		SpecVersion: specVersion, Reports: len(reports), Devices: len(devices), ModelPairs: len(pairs),
		Readiness: readiness,
	}
	for id, a := range byItem {
		status := "not_observed"
		if a.flips > 0 {
			status = "observed"
		}
		decisionStatus := "insufficient_evidence"
		reviewCandidate := readiness.ReadyForReview && a.decisionFlips == 0
		if readiness.ReadyForReview {
			decisionStatus = "observed"
			if reviewCandidate {
				decisionStatus = "review_candidate"
			}
		}
		s.Items = append(s.Items, SummaryItem{
			TaskID: id, Family: a.family, Need: a.need,
			Reports: a.reports, Devices: len(a.devices), Shared: a.shared, Flips: a.flips,
			DiscriminatedReports: a.discriminatedReports,
			DiscriminatedDevices: len(a.discriminatedDevices), Status: status,
			DecisionGradeReports: a.decisionReports,
			DecisionGradeDevices: len(a.decisionDevices),
			DecisionGradeShared:  a.decisionShared, DecisionGradeFlips: a.decisionFlips,
			DecisionGradeStatus: decisionStatus, ReviewCandidate: reviewCandidate,
		})
	}
	sort.Slice(s.Items, func(i, j int) bool {
		if s.Items[i].DiscriminatedDevices != s.Items[j].DiscriminatedDevices {
			return s.Items[i].DiscriminatedDevices > s.Items[j].DiscriminatedDevices
		}
		if s.Items[i].Flips != s.Items[j].Flips {
			return s.Items[i].Flips > s.Items[j].Flips
		}
		return s.Items[i].TaskID < s.Items[j].TaskID
	})
	return s, nil
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if !b[key] {
			return false
		}
	}
	return true
}
