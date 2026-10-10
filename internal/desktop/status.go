// Package desktop projects sealed role and analysis documents for a read-only
// shell surface. It does not score, rank, download, or contact a serving runtime.
package desktop

import (
	"errors"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/decision"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/role"
)

const Schema = "fitr.desktop.status.v1"

const (
	StateEmpty       = "empty"
	StateUnavailable = "unavailable"
	StateUnsupported = "unsupported"
	StateUnresolved  = "unresolved"
	StateUnselected  = "unselected"
	StateStale       = "stale"
	StateQualified   = "qualified"
)

const (
	EffectNone          = "none"
	EffectRead          = "read"
	EffectDisplay       = "display"
	EffectExplicitLocal = "explicit-local"
)

// Status is a presentation document. It is not evidence and must not be
// written back into a sealed record.
type Status struct {
	Schema          string       `json:"schema"`
	ReadOnly        bool         `json:"read_only"`
	Surface         string       `json:"surface"`
	State           string       `json:"state"`
	Role            string       `json:"role,omitempty"`
	Model           string       `json:"model,omitempty"`
	Roles           []string     `json:"roles,omitempty"`
	Rows            []Row        `json:"rows"`
	Unresolved      []Unresolved `json:"unresolved"`
	UnresolvedState string       `json:"unresolved_state"`
	Next            Next         `json:"next"`
	Limits          []string     `json:"limits"`
	Sources         []string     `json:"sources,omitempty"`
	FitrVersion     string       `json:"fitr_version,omitempty"`
}

// Row is one fact the shell can print without deciding what it means.
type Row struct {
	ID    string `json:"id"`
	Label string `json:"label"`
	Value string `json:"value"`
	State string `json:"state"`
}

// Unresolved is one requirement the existing decision evaluation left open.
type Unresolved struct {
	ID     string `json:"id"`
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"`
}

// Next is the one following action. LocalProven is true only when Argv is a
// local measurement the desktop is willing to start after an explicit confirm.
type Next struct {
	Argv        []string `json:"argv,omitempty"`
	Text        string   `json:"text,omitempty"`
	Effect      string   `json:"effect"`
	Reason      string   `json:"reason,omitempty"`
	LocalProven bool     `json:"local_proven"`
}

// Evidence is the already-decided input. Nil reports stay nil; Project does
// not invent the missing observation.
type Evidence struct {
	RoleName     string
	NameInvalid  bool
	Libraries    []role.Library
	ListErr      error
	MissingRole  bool
	Library      role.Library
	Review       *role.ReviewReport
	ReviewErr    error
	Selection    *role.SelectionStatus
	Record       *record.Record
	RecordErr    error
	Report       *analysis.Report
	ReportErr    error
	AmbientRoute bool
}

// Load reads one role library and the canonical record that library already
// pinned. It does not probe a runtime and it does not create a role.
func Load(roles role.Store, records record.Store, name string, now time.Time, environ []string) Status {
	evidence := Evidence{RoleName: name, AmbientRoute: ambientRoute(environ)}
	if name != "" && !role.ValidName(name) {
		evidence.NameInvalid = true
		return Project(evidence)
	}
	if now.IsZero() {
		evidence.ListErr = errors.New("status requires a clock")
		return Project(evidence)
	}
	libraries, err := roles.List()
	evidence.Libraries = libraries
	evidence.ListErr = err
	if err != nil {
		return Project(evidence)
	}
	chosen, single := chosenRole(name, libraries)
	if !single {
		return Project(evidence)
	}
	evidence.RoleName = chosen
	loadRole(&evidence, roles, records, chosen, now)
	return Project(evidence)
}

func chosenRole(name string, libraries []role.Library) (string, bool) {
	if name != "" {
		return name, true
	}
	if len(libraries) != 1 {
		return "", false
	}
	return libraries[0].Name, true
}

func loadRole(evidence *Evidence, roles role.Store, records record.Store, name string, now time.Time) {
	library, err := roles.Load(name)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			evidence.MissingRole = true
			return
		}
		evidence.ReviewErr = err
		return
	}
	evidence.Library = library
	review, err := role.Review(library, records, now)
	if err != nil {
		evidence.ReviewErr = err
		return
	}
	evidence.Review = &review
	selection, err := roles.ReviewSelection(name, records, now)
	if err != nil {
		evidence.ReviewErr = err
		return
	}
	evidence.Selection = &selection
	loadPinned(evidence, library, records)
}

func loadPinned(evidence *Evidence, library role.Library, records record.Store) {
	attachment, ok := pinnedAttachment(library, evidence.Selection, evidence.Review)
	if !ok {
		return
	}
	result, err := role.ReadAttachedRecord(attachment, records)
	evidence.Record = result
	evidence.RecordErr = err
	if err != nil {
		return
	}
	report, err := analysis.FromRecord(result)
	evidence.ReportErr = err
	if err != nil {
		return
	}
	evidence.Report = &report
}

// Project copies states that role review, selection status, and analysis have
// already decided. A missing prediction or budget stays unmeasured.
func Project(in Evidence) Status {
	status := Status{
		Schema: Schema, ReadOnly: true, Surface: "omarchy",
		Unresolved: []Unresolved{}, Limits: limits(),
	}
	switch {
	case in.NameInvalid:
		status.State = StateUnsupported
		status.Next = Next{Effect: EffectNone, Reason: "The role name is not valid for the role store."}
	case in.ListErr != nil || in.ReviewErr != nil:
		status.State = StateUnavailable
		status.Next = Next{Effect: EffectNone, Reason: safeReason(firstErr(in.ListErr, in.ReviewErr), "The role store could not be read.")}
	case in.MissingRole:
		status.State = StateEmpty
		status.Role = in.RoleName
		status.Next = Next{Effect: EffectRead, Argv: []string{"fitr", "role", "list"}, Text: "fitr role list", Reason: "The named role is not stored."}
	case in.RoleName == "" && len(in.Libraries) == 0:
		status.State = StateEmpty
		status.Next = Next{Effect: EffectRead, Argv: []string{"fitr", "role", "list"}, Text: "fitr role list", Reason: "No role is stored, so there is no declared work to judge."}
	case in.RoleName == "" && len(in.Libraries) > 1:
		status.State = StateUnresolved
		status.Roles = roleNames(in.Libraries)
		status.Next = Next{Effect: EffectRead, Reason: "Name one role. The desktop does not choose among roles."}
	default:
		fillRole(&status, in)
	}
	status.Rows = rows(status, in)
	return status
}

func fillRole(status *Status, in Evidence) {
	status.Role = in.RoleName
	status.Model = resolvedModel(in)
	status.Sources = sources(in)
	status.Unresolved, status.UnresolvedState = unresolved(in)
	status.State = roleState(in)
	status.Next = nextAction(in, status.Model)
	if in.RecordErr != nil && status.State != StateUnavailable {
		status.Next.Reason = joinReason(status.Next.Reason, "The pinned evidence could not be read, so fit stays unmeasured.")
	}
	if in.ReportErr != nil {
		status.State = StateUnsupported
		status.Next.Effect = EffectDisplay
		status.Next.LocalProven = false
		status.Next.Reason = joinReason(status.Next.Reason, "The sealed record is not an analysis this desktop can project.")
	}
}

func roleState(in Evidence) string {
	if in.ReportErr != nil {
		return StateUnsupported
	}
	selection := ""
	if in.Selection != nil {
		selection = in.Selection.State
	}
	candidate := matchedCandidate(in)
	candidateState := ""
	if candidate != nil {
		candidateState = candidate.State
	}
	if selection == "stale" || candidateState == "stale" || in.RecordErr != nil {
		return StateStale
	}
	if selection == "qualified" && candidateState == "eligible" {
		return StateQualified
	}
	if selection == "" || selection == "unselected" {
		if candidateState == "unresolved" || candidateState == "ineligible" || statusHasOpenRequirements(in) {
			return StateUnresolved
		}
		return StateUnselected
	}
	return StateUnresolved
}

func statusHasOpenRequirements(in Evidence) bool {
	items, state := unresolved(in)
	return state == "listed" && len(items) > 0
}

func resolvedModel(in Evidence) string {
	if in.Selection != nil && in.Selection.Selection != nil {
		if name := strings.TrimSpace(in.Selection.Selection.Selected.Model.Resolved); name != "" {
			return name
		}
	}
	if in.Record != nil && in.Record.Manifest != nil {
		return strings.TrimSpace(in.Record.Manifest.Model.Resolved)
	}
	return ""
}

func matchedCandidate(in Evidence) *role.Candidate {
	if in.Review == nil {
		return nil
	}
	if sha := selectedSHA(in.Selection); sha != "" {
		for index := range in.Review.Candidates {
			if in.Review.Candidates[index].ID == sha {
				return &in.Review.Candidates[index]
			}
		}
		return nil
	}
	if len(in.Review.Candidates) == 1 {
		return &in.Review.Candidates[0]
	}
	return nil
}

func selectedSHA(selection *role.SelectionStatus) string {
	if selection == nil || selection.Selection == nil {
		return ""
	}
	return selection.Selection.Selected.Attachment.EvidenceSHA256
}

func pinnedAttachment(library role.Library, selection *role.SelectionStatus, review *role.ReviewReport) (role.Attachment, bool) {
	if selection != nil && selection.Selection != nil {
		attachment := selection.Selection.Selected.Attachment
		if attachment.EvidenceSHA256 != "" && attachment.Path != "" {
			return attachment, true
		}
	}
	if review == nil || len(review.Candidates) != 1 {
		return role.Attachment{}, false
	}
	for _, attachment := range library.Candidates {
		if attachment.EvidenceSHA256 == review.Candidates[0].ID {
			return attachment, true
		}
	}
	return role.Attachment{}, false
}

func unresolved(in Evidence) ([]Unresolved, string) {
	candidate := matchedCandidate(in)
	if candidate == nil || candidate.Evaluation == nil {
		if candidate == nil && in.Review != nil && len(in.Review.Candidates) > 1 {
			return []Unresolved{}, "unmeasured"
		}
		if candidate == nil {
			return []Unresolved{}, "none"
		}
		return []Unresolved{}, "unmeasured"
	}
	items := make([]Unresolved, 0)
	for _, requirement := range candidate.Evaluation.Requirements {
		if requirement.State == decision.RequirementEstablished {
			continue
		}
		items = append(items, Unresolved{
			ID: requirement.ID, State: string(requirement.State), Reason: requirement.Reason,
		})
	}
	if len(items) == 0 {
		return []Unresolved{}, "none"
	}
	return items, "listed"
}

func nextAction(in Evidence, model string) Next {
	action := Next{Effect: EffectNone}
	if in.Report != nil && len(in.Report.NextActions) > 0 {
		source := in.Report.NextActions[0]
		argv, replaced := substituteModel(source.Argv, model)
		action.Argv = argv
		action.Text = formatArgv(argv)
		action.Reason = source.Reason
		local := replaced && !in.AmbientRoute && localProtocol(in.Record)
		action.Effect, action.LocalProven = classify(argv, model, local)
		if in.AmbientRoute && allowLocalRun(argv, model) {
			action.Effect = EffectDisplay
			action.LocalProven = false
			action.Reason = joinReason(action.Reason, ambientRouteReason)
		}
		if !replaced && strings.Contains(action.Text, analysis.CurrentModelPlaceholder) {
			action.Effect = EffectDisplay
			action.LocalProven = false
		}
		return action
	}
	if in.Review != nil && in.Review.Next != "" {
		action.Effect = EffectDisplay
		action.Reason = in.Review.Next
	}
	return action
}

func rows(status Status, in Evidence) []Row {
	modelValue, modelState := "unmeasured", "unmeasured"
	if status.Model != "" {
		modelValue, modelState = status.Model, "resolved"
	}
	roleValue, roleState := "unmeasured", "unmeasured"
	if status.Role != "" {
		roleValue = status.Role
		roleState = status.State
	}
	requestedValue, requestedState := requestedContext(in.Report)
	effectiveValue, effectiveState := effectiveContext(in.Report)
	estimatedValue, estimatedState := estimatedFit(in.Report)
	measuredValue, measuredState := measuredFit(in.Report)
	freshValue, freshState := freshness(in)
	return []Row{
		{ID: "model", Label: "Model", Value: modelValue, State: modelState},
		{ID: "role", Label: "Role", Value: roleValue, State: roleState},
		{ID: "requested_context", Label: "Requested context", Value: requestedValue, State: requestedState},
		{ID: "effective_context", Label: "Effective context", Value: effectiveValue, State: effectiveState},
		{ID: "estimated_fit", Label: "Estimated fit", Value: estimatedValue, State: estimatedState},
		{ID: "measured_fit", Label: "Measured fit", Value: measuredValue, State: measuredState},
		{ID: "freshness", Label: "Evidence freshness", Value: freshValue, State: freshState},
	}
}

func requestedContext(report *analysis.Report) (string, string) {
	if report == nil || report.Context.Requested <= 0 {
		return "unmeasured", "unmeasured"
	}
	state := report.Context.State
	if state == "" {
		state = "recorded"
	}
	return strconv.Itoa(report.Context.Requested) + " tokens", state
}

func effectiveContext(report *analysis.Report) (string, string) {
	if report == nil || report.Context.Effective == nil {
		return "unmeasured", "unmeasured"
	}
	return strconv.Itoa(*report.Context.Effective) + " tokens", "recorded"
}

func estimatedFit(report *analysis.Report) (string, string) {
	if report == nil || report.Capacity.Prediction == nil {
		return "unmeasured", "unmeasured"
	}
	prediction := report.Capacity.Prediction
	switch prediction.Status {
	case analysis.StatusDescriptiveOnly:
		return "descriptive only", string(prediction.Status)
	case analysis.StatusAvailable:
	default:
		return "unmeasured", "unmeasured"
	}
	if len(prediction.Missing) > 0 {
		return "incomplete", "incomplete"
	}
	parts := []string{"projected"}
	if prediction.ArtifactBytes != nil {
		parts = append(parts, "weights "+strconv.FormatInt(*prediction.ArtifactBytes, 10)+" bytes")
	}
	if prediction.KVBytes != nil {
		parts = append(parts, "kv "+strconv.FormatInt(*prediction.KVBytes, 10)+" bytes")
	}
	if prediction.KnownComponentBytes != nil {
		parts = append(parts, "components "+strconv.FormatInt(*prediction.KnownComponentBytes, 10)+" bytes")
	}
	if len(parts) == 1 {
		return "unmeasured", "unmeasured"
	}
	return strings.Join(parts, "; "), "projected"
}

func measuredFit(report *analysis.Report) (string, string) {
	if report == nil || report.Capacity.Budget == nil {
		return "unmeasured", "unmeasured"
	}
	budget := report.Capacity.Budget
	if budget.Status == analysis.StatusDescriptiveOnly {
		return "descriptive only", string(budget.Status)
	}
	if budget.Status != analysis.StatusAvailable {
		return "unmeasured", "unmeasured"
	}
	switch budget.State {
	case analysis.CapacityBudgetFit, analysis.CapacityBudgetExceeded, analysis.CapacityBudgetUnresolved:
	default:
		return "unmeasured", "unmeasured"
	}
	value := string(budget.State)
	if budget.ObservedBytes != nil {
		value += "; " + strconv.FormatInt(*budget.ObservedBytes, 10) + " bytes"
	}
	return value, string(budget.State)
}

func freshness(in Evidence) (string, string) {
	if in.Selection != nil && in.Selection.State == "stale" {
		return staleFreshness(in.Selection), "stale"
	}
	if candidate := matchedCandidate(in); candidate != nil && candidate.State == "stale" {
		return "stale", "stale"
	}
	if in.RecordErr != nil {
		return "stale", "stale"
	}
	if in.Selection != nil && in.Selection.State == "qualified" {
		if in.Selection.EvaluatedAt != "" {
			return "fresh; checked " + in.Selection.EvaluatedAt, "fresh"
		}
		return "fresh", "fresh"
	}
	return "unknown", "unknown"
}

func staleFreshness(selection *role.SelectionStatus) string {
	if selection.Reason == "" {
		return "stale"
	}
	return "stale; " + selection.Reason
}

func sources(in Evidence) []string {
	var out []string
	if in.Review != nil {
		out = append(out, role.ReviewSchema)
	}
	if in.Selection != nil {
		out = append(out, role.LifecycleSchema+".status")
	}
	if in.Report != nil {
		out = append(out, analysis.ReportSchema)
	}
	return out
}

func roleNames(libraries []role.Library) []string {
	names := make([]string, 0, len(libraries))
	for _, library := range libraries {
		names = append(names, library.Name)
	}
	sort.Strings(names)
	return names
}

func limits() []string {
	return []string{
		"This status reads sealed local evidence. It does not download models, reconfigure serving, or call a model.",
		"A measurement starts only from fitr desktop benchmark --confirm, and only when that command can prove the run stays on a local runtime.",
		"The panel is not a ranking and not the native desktop application described in the interface direction.",
	}
}

func firstErr(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}

func safeReason(err error, fallback string) string {
	if err == nil {
		return fallback
	}
	if errors.Is(err, os.ErrNotExist) {
		return "The role is not stored."
	}
	text := err.Error()
	if text == "" || strings.Contains(text, "/") || strings.Contains(text, `\`) || strings.Contains(text, "sha256:") {
		return fallback
	}
	return text
}

func joinReason(left, right string) string {
	switch {
	case left == "":
		return right
	case right == "":
		return left
	default:
		return left + " " + right
	}
}

func substituteModel(argv []string, model string) ([]string, bool) {
	out := append([]string(nil), argv...)
	if strings.TrimSpace(model) == "" {
		return out, false
	}
	replaced := false
	for index, arg := range out {
		if arg == analysis.CurrentModelPlaceholder {
			out[index] = model
			replaced = true
		}
	}
	if replaced {
		return out, true
	}
	if len(out) >= 3 && out[0] == "fitr" && out[1] == "run" && out[2] == model {
		return out, true
	}
	return out, len(out) > 0 && !containsPlaceholder(out)
}

func containsPlaceholder(argv []string) bool {
	for _, arg := range argv {
		if arg == analysis.CurrentModelPlaceholder {
			return true
		}
	}
	return false
}

func classify(argv []string, model string, local bool) (string, bool) {
	if len(argv) >= 2 && argv[0] == "fitr" && (argv[1] == "board" || argv[1] == "role" || argv[1] == "desktop") {
		return EffectRead, false
	}
	if local && allowLocalRun(argv, model) {
		return EffectExplicitLocal, true
	}
	if len(argv) > 0 {
		return EffectDisplay, false
	}
	return EffectNone, false
}

func localProtocol(result *record.Record) bool {
	if result == nil || result.Manifest == nil {
		return false
	}
	backend := strings.TrimSpace(result.Manifest.Model.Backend)
	protocol := strings.TrimSpace(result.Manifest.Provenance.BackendProtocol)
	if protocol == "" || !recordProtocolLocal(protocol) {
		return false
	}
	return strings.EqualFold(protocol, record.BackendProtocol(backend))
}

func recordProtocolLocal(protocol string) bool {
	switch strings.ToLower(strings.TrimSpace(protocol)) {
	case strings.ToLower(record.BackendProtocolOllama), strings.ToLower(record.BackendProtocolLlamaServerNative):
		return true
	default:
		return false
	}
}

// ambientRoute reports configuration that can send a stock `fitr run` off this
// machine. OpenAI variables are refused outright. OLLAMA_BASE_URL and
// LLAMA_SERVER_URL are the client URLs internal/ollama and internal/llamaserver
// read; --backend does not override them, so a value that is not a loopback
// http URL would turn an allowlisted local argv into a remote run. Loopback
// still does not prove local inference; it only keeps the desktop from
// starting a run it can already see is remote.
func ambientRoute(environ []string) bool {
	for _, entry := range environ {
		key, value, ok := strings.Cut(entry, "=")
		if !ok || strings.TrimSpace(value) == "" {
			continue
		}
		switch key {
		case "FITR_OPENAI_URL", "FITR_OPENAI_API_KEY", "OPENAI_API_KEY":
			return true
		case "OLLAMA_BASE_URL", "LLAMA_SERVER_URL":
			if !loopbackURL(value) {
				return true
			}
		}
	}
	return false
}

func loopbackURL(value string) bool {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "http" || parsed.User != nil {
		return false
	}
	switch parsed.Hostname() {
	case "127.0.0.1", "localhost", "::1":
		return true
	}
	return false
}
