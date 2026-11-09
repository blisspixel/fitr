package experiment

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strconv"
	"strings"
	"unicode"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const (
	ModelSetPlanSchema        = "fitr.experiment.modelset.plan.v1"
	ModelSetObservationSchema = "fitr.experiment.modelset.observation.v1"
	ModelSetReportSchema      = "fitr.experiment.modelset.analysis.v1"
	ModelSetBundleSchema      = "fitr.experiment.modelset.bundle.v1"

	ModelSetClaimUnresolved = "unresolved"

	ModelSetRuntimeOllama      = "ollama"
	ModelSetRuntimeLlamaServer = "llama-server"
	ModelSetRuntimeOpenAI      = "openai"

	ObservationOllamaPS            = "ollama_ps"
	ObservationSeparateCalls       = "separate_calls"
	ObservationLlamaProps          = "llama_props"
	ObservationLlamaRouter         = "llama_router"
	ObservationOpenAIUnavailable   = "openai_ps_unavailable"
	ObservationIsolatedAllocations = "isolated_allocations"
	ObservationSpill               = "spill"
	ObservationMalformed           = "malformed"

	DispositionNotOneObservation = "not_one_observation"
	DispositionSeparateCalls     = "separate_calls"
	DispositionBytesUnmeasured   = "bytes_unmeasured"
	DispositionCandidateUnproven = "candidate_unproven"
	DispositionUnavailable       = "unavailable"
	DispositionAdditive          = "additive_projection"
	DispositionSpill             = "spill"
	DispositionUnmeasured        = "unmeasured"
	DispositionRuntimeMismatch   = "runtime_mismatch"

	minModelSetMembers = 2
	maxModelSetMembers = 8
	maxModelSetContext = 16 * 1024 * 1024
	maxModelSetNote    = 256
	maxRowName         = 256
)

var modelSetDigest = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ModelSetMember is one declared participant. PlacementIntention is the
// operator's intention, not an observed placement.
type ModelSetMember struct {
	ArtifactDigest     string `json:"artifact_digest"`
	Role               string `json:"role"`
	RequestedContext   int    `json:"requested_context"`
	PlacementIntention string `json:"placement_intention"`
}

// ModelSetPlan binds an ordered set before any observation is read. Stage
// stays explore: this plan cannot certify a co-resident winner.
type ModelSetPlan struct {
	Schema               string           `json:"schema"`
	PlanSHA256           string           `json:"plan_sha256"`
	Stage                Stage            `json:"stage"`
	Runtime              string           `json:"runtime"`
	DeviceKey            string           `json:"device_key"`
	CapacityPolicySHA256 string           `json:"capacity_policy_sha256,omitempty"`
	Members              []ModelSetMember `json:"members"`
}

// ResidentRow is one recorded runner line. Sizes are the runtime's own
// figures when the body carried them. They are not an independent residency
// reading, and they are not added together.
type ResidentRow struct {
	Name      string `json:"name"`
	Digest    string `json:"digest,omitempty"`
	SizeBytes *int64 `json:"size_bytes,omitempty"`
	VRAMBytes *int64 `json:"vram_bytes,omitempty"`
	State     string `json:"state,omitempty"`
}

// ModelSetObservation is a recorded body, not a claim that the body was one
// simultaneous read. Kind says which wire shape was saved. Bodies says how
// many HTTP responses were combined, when the kind has a count.
type ModelSetObservation struct {
	Schema       string        `json:"schema"`
	Kind         string        `json:"kind"`
	Bodies       int           `json:"bodies,omitempty"`
	Rows         []ResidentRow `json:"rows,omitempty"`
	HandlerProof string        `json:"handler_proof,omitempty"`
}

// ModelSetReport is derived from the plan and the observation. CoResident is
// present and false. A stored true cannot survive validation, and a missing
// value is not the same document as an explicit refusal.
type ModelSetReport struct {
	Schema           string   `json:"schema"`
	Stage            Stage    `json:"stage"`
	PlanSHA256       string   `json:"plan_sha256"`
	ObservationKind  string   `json:"observation_kind"`
	Claim            string   `json:"claim"`
	CoResident       *bool    `json:"co_resident"`
	Disposition      string   `json:"disposition"`
	Reason           string   `json:"reason"`
	MemberCount      int      `json:"member_count"`
	DoesNotEstablish []string `json:"does_not_establish"`
	Gaps             []string `json:"gaps,omitempty"`
}

// ModelSetBundle is the local replay document. The stored report is checked
// against a fresh derivation and is not a schema-6 run record.
type ModelSetBundle struct {
	Schema      string              `json:"schema"`
	Plan        ModelSetPlan        `json:"plan"`
	Observation ModelSetObservation `json:"observation"`
	Report      ModelSetReport      `json:"report"`
}

// NewModelSetPlan seals the ordered members. It does not read a runtime.
func NewModelSetPlan(runtime, deviceKey, capacityPolicy string, members []ModelSetMember) (ModelSetPlan, error) {
	plan := ModelSetPlan{
		Schema: ModelSetPlanSchema, Stage: StageExplore,
		Runtime: strings.TrimSpace(runtime), DeviceKey: strings.TrimSpace(deviceKey),
		CapacityPolicySHA256: strings.TrimSpace(capacityPolicy),
		Members:              append([]ModelSetMember(nil), members...),
	}
	if err := plan.validateWithoutDigest(); err != nil {
		return ModelSetPlan{}, err
	}
	digest, err := modelSetPlanDigest(plan)
	if err != nil {
		return ModelSetPlan{}, err
	}
	plan.PlanSHA256 = digest
	return plan, nil
}

func (plan ModelSetPlan) Validate() error {
	if err := plan.validateWithoutDigest(); err != nil {
		return err
	}
	digest, err := modelSetPlanDigest(plan)
	if err != nil {
		return err
	}
	if digest != plan.PlanSHA256 {
		return errors.New("model-set plan digest does not match")
	}
	return nil
}

func (plan ModelSetPlan) validateWithoutDigest() error {
	if plan.Schema != ModelSetPlanSchema || plan.Stage != StageExplore {
		return errors.New("unsupported model-set plan schema or stage")
	}
	if !knownModelSetRuntime(plan.Runtime) {
		return errors.New("model-set plan runtime is not ollama, llama-server, or openai")
	}
	if err := plainToken(plan.DeviceKey, 8, 512); err != nil {
		return fmt.Errorf("model-set plan device key: %w", err)
	}
	if plan.CapacityPolicySHA256 != "" && !modelSetDigest.MatchString(plan.CapacityPolicySHA256) {
		return errors.New("model-set capacity policy digest is invalid")
	}
	if len(plan.Members) < minModelSetMembers || len(plan.Members) > maxModelSetMembers {
		return fmt.Errorf("model-set plan needs %d to %d members", minModelSetMembers, maxModelSetMembers)
	}
	seen := make(map[string]bool, len(plan.Members))
	for _, member := range plan.Members {
		if err := member.validate(); err != nil {
			return err
		}
		key := member.ArtifactDigest + "\x00" + member.Role + "\x00" +
			strconv.Itoa(member.RequestedContext) + "\x00" + member.PlacementIntention
		if seen[key] {
			return errors.New("model-set plan repeats a member")
		}
		seen[key] = true
	}
	return nil
}

func (member ModelSetMember) validate() error {
	if !modelSetDigest.MatchString(member.ArtifactDigest) {
		return errors.New("model-set member artifact digest is invalid")
	}
	if err := token(member.Role); err != nil {
		return fmt.Errorf("model-set member role: %w", err)
	}
	if err := token(member.PlacementIntention); err != nil {
		return fmt.Errorf("model-set member placement intention: %w", err)
	}
	if member.RequestedContext < 1 || member.RequestedContext > maxModelSetContext {
		return errors.New("model-set member requested context is outside the supported range")
	}
	return nil
}

func modelSetPlanDigest(plan ModelSetPlan) (string, error) {
	plan.PlanSHA256 = ""
	return factorDigest("modelset_plan", plan)
}

// AnalyzeModelSet derives the only legal claim in this slice: co-residency
// is unresolved. It does not add row sizes, and it does not read a runtime.
func AnalyzeModelSet(plan ModelSetPlan, observation ModelSetObservation) (ModelSetReport, error) {
	if err := plan.Validate(); err != nil {
		return ModelSetReport{}, err
	}
	if err := observation.validateShape(); err != nil {
		return ModelSetReport{}, err
	}
	disposition, reason, gaps := classifyModelSet(plan, observation)
	return newModelSetReport(plan, observation, disposition, reason, gaps), nil
}

func newModelSetReport(plan ModelSetPlan, observation ModelSetObservation, disposition, reason string, gaps []string) ModelSetReport {
	coResident := false
	report := ModelSetReport{
		Schema: ModelSetReportSchema, Stage: StageExplore, PlanSHA256: plan.PlanSHA256,
		ObservationKind: observation.Kind, Claim: ModelSetClaimUnresolved, CoResident: &coResident,
		Disposition: disposition, Reason: reason, MemberCount: len(plan.Members),
		DoesNotEstablish: modelSetRefusals(),
	}
	if cleaned := uniqueSorted(gaps); len(cleaned) > 0 {
		report.Gaps = cleaned
	}
	return report
}

func modelSetRefusals() []string {
	return []string{
		"additive_fit",
		"exact_context_other",
		"simultaneous_residency",
		"single_model_board",
		"spill_or_eviction_receipt",
	}
}

func (observation ModelSetObservation) validateShape() error {
	if observation.Schema != ModelSetObservationSchema || !knownObservationKind(observation.Kind) {
		return errors.New("unsupported model-set observation schema or kind")
	}
	if observation.Bodies < 0 || observation.Bodies > maxModelSetMembers {
		return errors.New("model-set observation body count is outside the supported range")
	}
	if len(observation.Rows) > maxModelSetMembers {
		return errors.New("model-set observation has too many rows")
	}
	if len(observation.HandlerProof) > maxModelSetNote || hasControl(observation.HandlerProof) {
		return errors.New("model-set handler note is not a bounded plain token")
	}
	return nil
}

func classifyModelSet(plan ModelSetPlan, observation ModelSetObservation) (string, string, []string) {
	gaps := countGap(plan, observation)
	if !runtimeAdmits(plan.Runtime, observation.Kind) {
		return DispositionRuntimeMismatch,
			"the recorded observation is not from the runtime named in the sealed plan", gaps
	}
	if !bodiesMatchKind(observation.Kind, observation.Bodies) {
		return DispositionUnmeasured,
			"the observation kind and its body count disagree, so the rows are not summed", gaps
	}
	if problem := rowProblem(observation); problem != "" {
		return DispositionUnmeasured, problem, gaps
	}
	if disposition, reason, ok := spillDisposition(observation); ok {
		if observation.Kind == ObservationOllamaPS {
			gaps = append(gaps, "the process-list body is still not one simultaneous observation")
		}
		return disposition, reason, gaps
	}
	if observation.Kind == ObservationLlamaRouter && observation.HandlerProof != "" {
		gaps = append(gaps, "a handler note was recorded and does not establish the read")
	}
	disposition, reason := kindDisposition(observation.Kind)
	return disposition, reason, gaps
}

func countGap(plan ModelSetPlan, observation ModelSetObservation) []string {
	if len(observation.Rows) == 0 || len(observation.Rows) == len(plan.Members) {
		return nil
	}
	return []string{"recorded row count differs from the sealed member count"}
}

func spillDisposition(observation ModelSetObservation) (string, string, bool) {
	if len(observation.Rows) == 0 || !allSpill(observation.Rows) {
		return "", "", false
	}
	return DispositionSpill, "a sleeping or offloaded model is spill, not residency", true
}

func kindDisposition(kind string) (string, string) {
	switch kind {
	case ObservationOllamaPS:
		return DispositionNotOneObservation,
			"an Ollama process list copies membership under one lock and reads each runner's bytes afterward, so this body is not one observation"
	case ObservationSeparateCalls:
		return DispositionSeparateCalls, "separate responses are not one observation of membership and bytes"
	case ObservationLlamaProps:
		return DispositionBytesUnmeasured,
			"llama-server props does not report resident bytes; a zero or absent size is not a measurement and not an empty set"
	case ObservationLlamaRouter:
		return DispositionCandidateUnproven,
			"a llama-server router response is a candidate until its handler is shown to read membership and bytes together"
	case ObservationOpenAIUnavailable:
		return DispositionUnavailable,
			"an empty OpenAI-compatible process list is unavailable, not proof that no model is resident"
	case ObservationIsolatedAllocations:
		return DispositionAdditive, "isolated measurements are an additive projection, not co-residency"
	case ObservationSpill:
		return DispositionUnmeasured, "a spill observation needs a sleeping or offloaded row"
	default:
		return DispositionUnmeasured, "the recorded rows are not a usable observation of membership and bytes"
	}
}

func rowProblem(observation ModelSetObservation) string {
	if observation.Kind == ObservationOpenAIUnavailable {
		if len(observation.Rows) > 0 {
			return "an unavailable process list does not carry rows"
		}
		return ""
	}
	if observation.Kind == ObservationMalformed {
		return "the recorded rows are not a usable observation of membership and bytes"
	}
	names := make(map[string]bool, len(observation.Rows))
	sized, unsized := 0, 0
	spill, other := 0, 0
	for _, row := range observation.Rows {
		if problem := oneRowProblem(observation.Kind, row); problem != "" {
			return problem
		}
		if names[row.Name] {
			return "a recorded observation repeats a row name"
		}
		names[row.Name] = true
		if row.SizeBytes == nil {
			unsized++
		} else {
			sized++
		}
		if spillState(row.State) {
			spill++
		} else {
			other++
		}
	}
	if spill > 0 && other > 0 {
		return "sleeping and resident rows are mixed, and neither is dropped to force a count"
	}
	// A sleeping row is spill even when the body omitted its size. The size
	// check below is only for a list that might otherwise be summed.
	if spill > 0 {
		return ""
	}
	if sized > 0 && unsized > 0 {
		return "byte figures are partial, and a partial list is not summed"
	}
	return ""
}

func oneRowProblem(kind string, row ResidentRow) string {
	if row.Name == "" || len(row.Name) > maxRowName || hasControl(row.Name) || strings.TrimSpace(row.Name) != row.Name {
		return "a recorded row name is missing or not plain"
	}
	if row.Digest != "" && !modelSetDigest.MatchString(row.Digest) {
		return "a recorded row digest is not a sha256"
	}
	if !knownRowState(row.State) {
		return "a recorded row state is not resident, sleeping, offloaded, or expired"
	}
	if row.SizeBytes != nil && *row.SizeBytes < 0 {
		return "a recorded row size is negative"
	}
	if row.VRAMBytes != nil && (*row.VRAMBytes < 0 || row.SizeBytes == nil || *row.VRAMBytes > *row.SizeBytes) {
		return "a recorded accelerator size is negative, unpaired, or larger than the row total"
	}
	if kind == ObservationLlamaProps && ((row.SizeBytes != nil && *row.SizeBytes != 0) || row.VRAMBytes != nil) {
		return "llama-server props cannot carry a resident size"
	}
	if kind == ObservationSpill && !spillState(row.State) {
		return "a spill observation needs a sleeping or offloaded row"
	}
	return ""
}

func bodiesMatchKind(kind string, bodies int) bool {
	switch kind {
	case ObservationOllamaPS:
		return bodies == 1
	case ObservationSeparateCalls:
		return bodies >= minModelSetMembers && bodies <= maxModelSetMembers
	default:
		return bodies == 0
	}
}

func runtimeAdmits(runtime, kind string) bool {
	switch kind {
	case ObservationOllamaPS:
		return runtime == ModelSetRuntimeOllama
	case ObservationLlamaProps, ObservationLlamaRouter:
		return runtime == ModelSetRuntimeLlamaServer
	case ObservationOpenAIUnavailable:
		return runtime == ModelSetRuntimeOpenAI
	default:
		return knownModelSetRuntime(runtime)
	}
}

func knownModelSetRuntime(runtime string) bool {
	switch runtime {
	case ModelSetRuntimeOllama, ModelSetRuntimeLlamaServer, ModelSetRuntimeOpenAI:
		return true
	default:
		return false
	}
}

func knownObservationKind(kind string) bool {
	switch kind {
	case ObservationOllamaPS, ObservationSeparateCalls, ObservationLlamaProps, ObservationLlamaRouter,
		ObservationOpenAIUnavailable, ObservationIsolatedAllocations, ObservationSpill, ObservationMalformed:
		return true
	default:
		return false
	}
}

func knownRowState(state string) bool {
	switch state {
	case "", "resident", "sleeping", "offloaded", "expired":
		return true
	default:
		return false
	}
}

func spillState(state string) bool {
	switch state {
	case "sleeping", "offloaded", "expired":
		return true
	default:
		return false
	}
}

func allSpill(rows []ResidentRow) bool {
	for _, row := range rows {
		if !spillState(row.State) {
			return false
		}
	}
	return true
}

func token(value string) error {
	if len(value) < 1 || len(value) > 64 {
		return errors.New("must be 1 to 64 characters")
	}
	for i, r := range value {
		ok := unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || (r == '-' && i > 0)
		if !ok {
			return errors.New("must be letters, digits, underscore, or an interior hyphen")
		}
	}
	return nil
}

func plainToken(value string, minLen, maxLen int) error {
	if len(value) < minLen || len(value) > maxLen || strings.TrimSpace(value) != value || hasControl(value) {
		return errors.New("must be a bounded plain token")
	}
	return nil
}

func hasControl(value string) bool {
	for _, r := range value {
		if unicode.IsControl(r) {
			return true
		}
	}
	return false
}

// NewModelSetBundle derives the report and stores it beside the inputs.
func NewModelSetBundle(plan ModelSetPlan, observation ModelSetObservation) (ModelSetBundle, error) {
	report, err := AnalyzeModelSet(plan, observation)
	if err != nil {
		return ModelSetBundle{}, err
	}
	return ModelSetBundle{
		Schema: ModelSetBundleSchema, Plan: plan, Observation: observation, Report: report,
	}, nil
}

func (bundle ModelSetBundle) Validate() (ModelSetReport, error) {
	if bundle.Schema != ModelSetBundleSchema {
		return ModelSetReport{}, fmt.Errorf("unsupported model-set bundle schema %q", bundle.Schema)
	}
	rebuilt, err := AnalyzeModelSet(bundle.Plan, bundle.Observation)
	if err != nil {
		return ModelSetReport{}, err
	}
	rebuiltJSON, rebuiltErr := json.Marshal(rebuilt)
	storedJSON, storedErr := json.Marshal(bundle.Report)
	if rebuiltErr != nil || storedErr != nil {
		return ModelSetReport{}, errors.New("model-set report could not be normalized")
	}
	if !bytes.Equal(rebuiltJSON, storedJSON) {
		return ModelSetReport{}, errors.New("stored model-set report does not match its plan and observation")
	}
	return rebuilt, nil
}

func (plan ModelSetPlan) JSON() ([]byte, error) {
	if err := plan.Validate(); err != nil {
		return nil, err
	}
	return marshalModelSet(plan)
}

func (bundle ModelSetBundle) JSON() ([]byte, error) {
	if _, err := bundle.Validate(); err != nil {
		return nil, err
	}
	return marshalModelSet(bundle)
}

func marshalModelSet(value any) ([]byte, error) {
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return nil, err
	}
	data = append(data, '\n')
	if len(data) > maximumBundleBytes {
		return nil, fmt.Errorf("model-set document exceeds %d bytes", maximumBundleBytes)
	}
	return data, nil
}

// DecodeModelSetMembers reads the ordered member file. It does not sort it.
func DecodeModelSetMembers(data []byte) ([]ModelSetMember, error) {
	var members []ModelSetMember
	if err := decodeModelSetValue(data, &members); err != nil {
		return nil, err
	}
	if members == nil {
		return nil, errors.New("model-set members are missing")
	}
	return members, nil
}

// DecodeModelSetPlan validates a sealed plan file.
func DecodeModelSetPlan(data []byte) (ModelSetPlan, error) {
	var plan ModelSetPlan
	if err := decodeModelSetValue(data, &plan); err != nil {
		return ModelSetPlan{}, err
	}
	if err := plan.Validate(); err != nil {
		return ModelSetPlan{}, err
	}
	return plan, nil
}

// DecodeModelSetObservation validates the shape of a recorded body.
func DecodeModelSetObservation(data []byte) (ModelSetObservation, error) {
	var observation ModelSetObservation
	if err := decodeModelSetValue(data, &observation); err != nil {
		return ModelSetObservation{}, err
	}
	if err := observation.validateShape(); err != nil {
		return ModelSetObservation{}, err
	}
	return observation, nil
}

// LoadModelSetBundle reads one local bundle and re-derives its report.
func LoadModelSetBundle(path string) (ModelSetBundle, error) {
	data, err := boundedio.ReadFile(path, maximumBundleBytes)
	if err != nil {
		return ModelSetBundle{}, err
	}
	return decodeModelSetBundle(data)
}

func decodeModelSetBundle(data []byte) (ModelSetBundle, error) {
	var bundle ModelSetBundle
	if err := decodeModelSetValue(data, &bundle); err != nil {
		return ModelSetBundle{}, err
	}
	if _, err := bundle.Validate(); err != nil {
		return ModelSetBundle{}, err
	}
	return bundle, nil
}

func decodeModelSetValue(data []byte, into any) error {
	if len(data) > maximumBundleBytes {
		return fmt.Errorf("model-set document exceeds %d bytes", maximumBundleBytes)
	}
	if err := strictjson.Validate(data); err != nil {
		return fmt.Errorf("invalid model-set JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(into); err != nil {
		return fmt.Errorf("invalid model-set JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("content after model-set JSON")
	}
	return nil
}
