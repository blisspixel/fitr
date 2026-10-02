package experiment

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"sort"
	"strings"

	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const (
	ServingPlanSchema   = "fitr.experiment.serving.plan.v1"
	ServingReportSchema = "fitr.experiment.serving.analysis.v1"
	ServingBundleSchema = "fitr.experiment.serving.bundle.v1"

	MinServingConcurrency = 1
	MaxServingConcurrency = 64
	MinServingRequests    = 1
	MaxServingRequests    = 1000
	MinServingWarmup      = 0
	MaxServingWarmup      = 50
	MinSampleSizeP95      = 20
	MinSampleSizeP50      = 3
	maximumServingContext = 16 * 1024 * 1024
)

type ConcurrencyState string

const (
	ConcurrencyVerified ConcurrencyState = "verified"
	ConcurrencyMismatch ConcurrencyState = "mismatch"
	ConcurrencyDeclared ConcurrencyState = "declared"
)

type ConcurrencyObservation struct {
	DeclaredLevel int              `json:"declared_level"`
	ObservedSlots *int             `json:"observed_slots,omitempty"`
	State         ConcurrencyState `json:"state"`
	Backend       string           `json:"backend"`
	Evidence      string           `json:"evidence"`
}

type ServingRequestObservation struct {
	Index                int    `json:"index"`
	IsWarmup             bool   `json:"is_warmup"`
	QueueDurationMillis  int64  `json:"queue_duration_millis"`
	ServerDurationMillis int64  `json:"server_duration_millis"`
	TTFTMillis           *int64 `json:"ttft_millis,omitempty"`
	PromptTokens         int    `json:"prompt_tokens"`
	CompletionTokens     int    `json:"completion_tokens"`
	AcceptedOutcome      bool   `json:"accepted_outcome"`
	RefusalOrFault       string `json:"refusal_or_fault,omitempty"`
}

type DistributionSummary struct {
	SampleSize int      `json:"sample_size"`
	MeanMillis float64  `json:"mean_millis"`
	MinMillis  int64    `json:"min_millis"`
	MaxMillis  int64    `json:"max_millis"`
	P50Millis  *float64 `json:"p50_millis,omitempty"`
	P95Millis  *float64 `json:"p95_millis,omitempty"`
}

type ServingThroughput struct {
	ExposureWallMillis     int64   `json:"exposure_wall_millis"`
	TotalPromptTokens      int     `json:"total_prompt_tokens"`
	TotalCompletionTokens  int     `json:"total_completion_tokens"`
	PromptTokensPerSec     float64 `json:"prompt_tokens_per_sec"`
	CompletionTokensPerSec float64 `json:"completion_tokens_per_sec"`
	TotalTokensPerSec      float64 `json:"total_tokens_per_sec"`
}

type PerRequestThroughput struct {
	SampleSize       int      `json:"sample_size"`
	MeanTokensPerSec float64  `json:"mean_tokens_per_sec"`
	MinTokensPerSec  float64  `json:"min_tokens_per_sec"`
	MaxTokensPerSec  float64  `json:"max_tokens_per_sec"`
	P50TokensPerSec  *float64 `json:"p50_tokens_per_sec,omitempty"`
	P95TokensPerSec  *float64 `json:"p95_tokens_per_sec,omitempty"`
}

type ServingWorkSummary struct {
	EvaluatedCount        int     `json:"evaluated_count"`
	AcceptedCount         int     `json:"accepted_count"`
	AcceptedRate          float64 `json:"accepted_rate"`
	AcceptedPerWallMinute float64 `json:"accepted_per_wall_minute"`
	FaultCount            int     `json:"fault_count"`
	RefusalCount          int     `json:"refusal_count"`
}

type ServingPlan struct {
	Schema           string               `json:"schema"`
	PlanSHA256       string               `json:"plan_sha256"`
	Model            record.ModelIdentity `json:"model"`
	DeviceKey        string               `json:"device_key"`
	Concurrency      int                  `json:"concurrency"`
	Requests         int                  `json:"requests"`
	WarmupRequests   int                  `json:"warmup_requests"`
	RequestedContext int                  `json:"requested_context"`
	SeedSet          string               `json:"seedset"`
	Level            string               `json:"level"`
}

type ServingReport struct {
	Schema               string                 `json:"schema"`
	Stage                Stage                  `json:"stage"`
	Plan                 *ServingPlan           `json:"plan,omitempty"`
	PlanSHA256           string                 `json:"plan_sha256,omitempty"`
	Predeclared          bool                   `json:"predeclared"`
	Model                record.ModelIdentity   `json:"model"`
	Concurrency          ConcurrencyObservation `json:"concurrency"`
	WarmupExcluded       int                    `json:"warmup_excluded"`
	TotalThroughput      ServingThroughput      `json:"total_throughput"`
	PerRequestThroughput PerRequestThroughput   `json:"per_request_throughput"`
	ClientQueue          DistributionSummary    `json:"client_queue"`
	ServerTiming         DistributionSummary    `json:"server_timing"`
	TTFT                 *DistributionSummary   `json:"ttft,omitempty"`
	AcceptedWork         ServingWorkSummary     `json:"accepted_work"`
	Gaps                 []string               `json:"gaps,omitempty"`
	NextAction           *Action                `json:"next_action,omitempty"`
}

type ServingBundle struct {
	Schema             string                      `json:"schema"`
	Plan               ServingPlan                 `json:"plan"`
	Concurrency        ConcurrencyObservation      `json:"concurrency"`
	Requests           []ServingRequestObservation `json:"requests"`
	ExposureWallMillis int64                       `json:"exposure_wall_millis"`
	Report             ServingReport               `json:"report"`
}

func NewServingPlan(model record.ModelIdentity, deviceKey string,
	concurrency, requests, warmup, requestedContext int) (ServingPlan, error) {
	seedBytes := make([]byte, 16)
	if _, err := rand.Read(seedBytes); err != nil {
		return ServingPlan{}, fmt.Errorf("create serving seed set: %w", err)
	}
	plan := ServingPlan{
		Schema: ServingPlanSchema, Model: model,
		DeviceKey: strings.TrimSpace(deviceKey), Concurrency: concurrency,
		Requests: requests, WarmupRequests: warmup,
		RequestedContext: requestedContext, SeedSet: "serving-" + hex.EncodeToString(seedBytes),
		Level: "serving",
	}
	if err := plan.validateWithoutDigest(); err != nil {
		return ServingPlan{}, err
	}
	digest, err := servingPlanDigest(plan)
	if err != nil {
		return ServingPlan{}, err
	}
	plan.PlanSHA256 = digest
	return plan, nil
}

func (plan ServingPlan) Validate() error {
	if err := plan.validateWithoutDigest(); err != nil {
		return err
	}
	digest, err := servingPlanDigest(plan)
	if err != nil {
		return err
	}
	if digest != plan.PlanSHA256 {
		return errors.New("serving plan digest does not match")
	}
	return nil
}

func (plan ServingPlan) validateWithoutDigest() error {
	if plan.Schema != ServingPlanSchema || plan.Level != "serving" {
		return errors.New("unsupported serving plan schema or level")
	}
	if strings.TrimSpace(plan.Model.Resolved) == "" {
		return errors.New("serving plan model is required")
	}
	if strings.TrimSpace(plan.DeviceKey) == "" {
		return errors.New("serving plan device key is required")
	}
	if plan.Concurrency < MinServingConcurrency || plan.Concurrency > MaxServingConcurrency {
		return fmt.Errorf("concurrency must be between %d and %d", MinServingConcurrency, MaxServingConcurrency)
	}
	if plan.Requests < MinServingRequests || plan.Requests > MaxServingRequests {
		return fmt.Errorf("requests must be between %d and %d", MinServingRequests, MaxServingRequests)
	}
	if plan.WarmupRequests < MinServingWarmup || plan.WarmupRequests > MaxServingWarmup {
		return fmt.Errorf("warmup requests must be between %d and %d", MinServingWarmup, MaxServingWarmup)
	}
	if plan.RequestedContext < 1 || plan.RequestedContext > maximumServingContext {
		return fmt.Errorf("requested context must be between 1 and %d", maximumServingContext)
	}
	return nil
}

func servingPlanDigest(plan ServingPlan) (string, error) {
	planCopy := plan
	planCopy.PlanSHA256 = ""
	return factorDigest("serving_plan", planCopy)
}

func ServingPlanBinding(planSHA256 string, concurrency int) record.ExperimentBinding {
	return record.ExperimentBinding{
		Schema:     record.ExperimentBindingSchema,
		Kind:       "serving",
		Stage:      string(StageExplore),
		PlanSHA256: planSHA256,
		PointCount: concurrency,
	}
}

func ObserveConcurrency(ctx context.Context, backend llm.Backend, declared int) ConcurrencyObservation {
	backendName := "unknown"
	if backend != nil {
		backendName = backend.Name()
	}
	obs := ConcurrencyObservation{
		DeclaredLevel: declared,
		Backend:       backendName,
		State:         ConcurrencyDeclared,
	}
	if backend == nil {
		obs.Evidence = "no serving backend provided; concurrency level is declared-only"
		return obs
	}
	observer, ok := backend.(llm.SlotObserver)
	if !ok {
		obs.Evidence = backendName + " does not expose slot state; concurrency level remains declared-only"
		return obs
	}
	slots, observed, err := observer.ObserveSlots(ctx)
	if err != nil {
		obs.Evidence = fmt.Sprintf("%s slot observation failed (%v); concurrency level remains declared-only", backendName, err)
		return obs
	}
	if !observed {
		obs.Evidence = backendName + " did not observe slot state; concurrency level remains declared-only"
		return obs
	}
	obs.ObservedSlots = &slots
	if slots == declared {
		obs.State = ConcurrencyVerified
		obs.Evidence = fmt.Sprintf("%s verified %d active slots matching declared concurrency", backendName, slots)
	} else {
		obs.State = ConcurrencyMismatch
		obs.Evidence = fmt.Sprintf("%s reported %d slots, mismatching declared concurrency %d", backendName, slots, declared)
	}
	return obs
}

func AnalyzeServing(plan ServingPlan, concurrency ConcurrencyObservation,
	requests []ServingRequestObservation, exposureWallMillis int64) (ServingReport, error) {
	warmup, evaluated, err := splitServingRequests(requests, exposureWallMillis)
	if err != nil {
		return ServingReport{}, err
	}
	totalTP, err := summarizeThroughput(evaluated, exposureWallMillis)
	if err != nil {
		return ServingReport{}, err
	}
	perReqTP := summarizePerRequestThroughput(evaluated)
	queueTiming := summarizeDistribution(extractDurations(evaluated, func(r ServingRequestObservation) int64 {
		return r.QueueDurationMillis
	}))
	serverTiming := summarizeDistribution(extractDurations(evaluated, func(r ServingRequestObservation) int64 {
		return r.ServerDurationMillis
	}))
	ttftTiming := summarizeTTFT(evaluated)
	work := summarizeWork(evaluated, exposureWallMillis)
	gaps, nextAction := deriveServingGaps(concurrency, len(evaluated), ttftTiming, work)

	return ServingReport{
		Schema:               ServingReportSchema,
		Stage:                StageExplore,
		Plan:                 &plan,
		PlanSHA256:           plan.PlanSHA256,
		Predeclared:          plan.PlanSHA256 != "",
		Model:                plan.Model,
		Concurrency:          concurrency,
		WarmupExcluded:       len(warmup),
		TotalThroughput:      totalTP,
		PerRequestThroughput: perReqTP,
		ClientQueue:          queueTiming,
		ServerTiming:         serverTiming,
		TTFT:                 ttftTiming,
		AcceptedWork:         work,
		Gaps:                 gaps,
		NextAction:           nextAction,
	}, nil
}

func splitServingRequests(requests []ServingRequestObservation, exposureWallMillis int64) (
	warmup, evaluated []ServingRequestObservation, err error) {
	if exposureWallMillis <= 0 {
		return nil, nil, errors.New("exposure wall duration must be positive")
	}
	for _, req := range requests {
		if req.QueueDurationMillis < 0 || req.ServerDurationMillis < 0 {
			return nil, nil, errors.New("negative queue or server duration")
		}
		if req.IsWarmup {
			warmup = append(warmup, req)
		} else {
			evaluated = append(evaluated, req)
		}
	}
	if len(evaluated) == 0 {
		return nil, nil, errors.New("no evaluated requests; warm-up cannot constitute serving evidence")
	}
	return warmup, evaluated, nil
}

func summarizeThroughput(evaluated []ServingRequestObservation, exposureWallMillis int64) (ServingThroughput, error) {
	wallSec := float64(exposureWallMillis) / 1000.0
	promptTokens, completionTokens := 0, 0
	for _, req := range evaluated {
		promptTokens += req.PromptTokens
		completionTokens += req.CompletionTokens
	}
	totalTokens := promptTokens + completionTokens
	return ServingThroughput{
		ExposureWallMillis:     exposureWallMillis,
		TotalPromptTokens:      promptTokens,
		TotalCompletionTokens:  completionTokens,
		PromptTokensPerSec:     roundServing(float64(promptTokens)/wallSec, 2),
		CompletionTokensPerSec: roundServing(float64(completionTokens)/wallSec, 2),
		TotalTokensPerSec:      roundServing(float64(totalTokens)/wallSec, 2),
	}, nil
}

func summarizePerRequestThroughput(evaluated []ServingRequestObservation) PerRequestThroughput {
	var rates []float64
	for _, req := range evaluated {
		if req.ServerDurationMillis > 0 && req.CompletionTokens > 0 {
			sec := float64(req.ServerDurationMillis) / 1000.0
			rates = append(rates, float64(req.CompletionTokens)/sec)
		}
	}
	if len(rates) == 0 {
		return PerRequestThroughput{}
	}
	sort.Float64s(rates)
	sum := 0.0
	for _, r := range rates {
		sum += r
	}
	mean := sum / float64(len(rates))
	result := PerRequestThroughput{
		SampleSize:       len(rates),
		MeanTokensPerSec: roundServing(mean, 2),
		MinTokensPerSec:  roundServing(rates[0], 2),
		MaxTokensPerSec:  roundServing(rates[len(rates)-1], 2),
	}
	if len(rates) >= MinSampleSizeP50 {
		p50 := roundServing(percentile(rates, 0.50), 2)
		result.P50TokensPerSec = &p50
	}
	if len(rates) >= MinSampleSizeP95 {
		p95 := roundServing(percentile(rates, 0.95), 2)
		result.P95TokensPerSec = &p95
	}
	return result
}

func extractDurations(requests []ServingRequestObservation, fn func(r ServingRequestObservation) int64) []int64 {
	out := make([]int64, len(requests))
	for i, req := range requests {
		out[i] = fn(req)
	}
	return out
}

func summarizeDistribution(values []int64) DistributionSummary {
	if len(values) == 0 {
		return DistributionSummary{}
	}
	floats := make([]float64, len(values))
	minVal, maxVal := values[0], values[0]
	sum := 0.0
	for i, v := range values {
		floats[i] = float64(v)
		sum += float64(v)
		if v < minVal {
			minVal = v
		}
		if v > maxVal {
			maxVal = v
		}
	}
	sort.Float64s(floats)
	mean := sum / float64(len(values))
	summary := DistributionSummary{
		SampleSize: len(values),
		MeanMillis: roundServing(mean, 2),
		MinMillis:  minVal,
		MaxMillis:  maxVal,
	}
	if len(floats) >= MinSampleSizeP50 {
		p50 := roundServing(percentile(floats, 0.50), 2)
		summary.P50Millis = &p50
	}
	if len(floats) >= MinSampleSizeP95 {
		p95 := roundServing(percentile(floats, 0.95), 2)
		summary.P95Millis = &p95
	}
	return summary
}

func summarizeTTFT(evaluated []ServingRequestObservation) *DistributionSummary {
	var ttfts []int64
	for _, req := range evaluated {
		if req.TTFTMillis != nil {
			ttfts = append(ttfts, *req.TTFTMillis)
		}
	}
	if len(ttfts) == 0 {
		return nil
	}
	summary := summarizeDistribution(ttfts)
	return &summary
}

func summarizeWork(evaluated []ServingRequestObservation, exposureWallMillis int64) ServingWorkSummary {
	accepted := 0
	faults, refusals := 0, 0
	for _, req := range evaluated {
		if req.AcceptedOutcome {
			accepted++
			continue
		}
		reason := strings.ToLower(req.RefusalOrFault)
		if strings.Contains(reason, "refusal") || strings.Contains(reason, "refused") {
			refusals++
		} else {
			faults++
		}
	}
	wallMinutes := float64(exposureWallMillis) / 60000.0
	rate := 0.0
	if len(evaluated) > 0 {
		rate = float64(accepted) / float64(len(evaluated))
	}
	perWallMin := 0.0
	if wallMinutes > 0 {
		perWallMin = float64(accepted) / wallMinutes
	}
	return ServingWorkSummary{
		EvaluatedCount:        len(evaluated),
		AcceptedCount:         accepted,
		AcceptedRate:          roundServing(rate, 4),
		AcceptedPerWallMinute: roundServing(perWallMin, 2),
		FaultCount:            faults,
		RefusalCount:          refusals,
	}
}

func percentile(sortedValues []float64, q float64) float64 {
	n := len(sortedValues)
	if n == 0 {
		return 0
	}
	if n == 1 {
		return sortedValues[0]
	}
	idx := q * float64(n-1)
	i := int(math.Floor(idx))
	j := int(math.Ceil(idx))
	if i == j {
		return sortedValues[i]
	}
	frac := idx - float64(i)
	return sortedValues[i] + frac*(sortedValues[j]-sortedValues[i])
}

func roundServing(v float64, places int) float64 {
	p := math.Pow(10, float64(places))
	return math.Round(v*p) / p
}

func deriveServingGaps(concurrency ConcurrencyObservation, evaluatedCount int,
	ttft *DistributionSummary, work ServingWorkSummary) ([]string, *Action) {
	var gaps []string
	switch concurrency.State {
	case ConcurrencyDeclared:
		gaps = append(gaps, "serving backend does not expose slot verification; concurrency level is declared-only")
	case ConcurrencyMismatch:
		slotCount := 0
		if concurrency.ObservedSlots != nil {
			slotCount = *concurrency.ObservedSlots
		}
		gaps = append(gaps, fmt.Sprintf("observed runtime slots (%d) mismatch declared concurrency (%d)",
			slotCount, concurrency.DeclaredLevel))
	}
	if evaluatedCount < MinSampleSizeP95 {
		gaps = append(gaps, fmt.Sprintf("tail distribution (p95) withheld: requires at least %d samples (observed %d)",
			MinSampleSizeP95, evaluatedCount))
	}
	if ttft != nil && ttft.SampleSize < MinSampleSizeP95 {
		gaps = append(gaps, fmt.Sprintf("TTFT tail distribution (p95) withheld: requires at least %d samples (observed %d)",
			MinSampleSizeP95, ttft.SampleSize))
	}
	if work.FaultCount > 0 {
		gaps = append(gaps, fmt.Sprintf("faults observed during serving run: %d request(s) failed", work.FaultCount))
	}
	if work.RefusalCount > 0 {
		gaps = append(gaps, fmt.Sprintf("model refusals observed during serving run: %d request(s) refused", work.RefusalCount))
	}

	var action *Action
	if concurrency.State != ConcurrencyVerified {
		action = &Action{
			Code:   "verify_runtime_slots",
			Reason: "run against a backend exposing slot verification (e.g. llama-server) to certify achieved concurrency",
		}
	} else if evaluatedCount < MinSampleSizeP95 {
		action = &Action{
			Code:   "increase_serving_samples",
			Argv:   []string{"fitr", "experiment", "serving", "<model>", "-n", "20"},
			Reason: "run with at least 20 evaluated requests to measure p95 latency tails",
		}
	}
	return gaps, action
}

func NewServingBundle(plan ServingPlan, concurrency ConcurrencyObservation,
	requests []ServingRequestObservation, exposureWallMillis int64) (ServingBundle, error) {
	report, err := AnalyzeServing(plan, concurrency, requests, exposureWallMillis)
	if err != nil {
		return ServingBundle{}, err
	}
	return ServingBundle{
		Schema:             ServingBundleSchema,
		Plan:               plan,
		Concurrency:        concurrency,
		Requests:           append([]ServingRequestObservation(nil), requests...),
		ExposureWallMillis: exposureWallMillis,
		Report:             report,
	}, nil
}

func (bundle ServingBundle) Validate() (ServingReport, error) {
	if bundle.Schema != ServingBundleSchema {
		return ServingReport{}, fmt.Errorf("unsupported serving bundle schema %q", bundle.Schema)
	}
	rebuilt, err := AnalyzeServing(bundle.Plan, bundle.Concurrency, bundle.Requests, bundle.ExposureWallMillis)
	if err != nil {
		return ServingReport{}, err
	}
	rebuiltJSON, rebuiltErr := json.Marshal(rebuilt)
	storedJSON, storedErr := json.Marshal(bundle.Report)
	if rebuiltErr != nil || storedErr != nil {
		return ServingReport{}, errors.New("serving report could not be normalized")
	}
	if !bytes.Equal(rebuiltJSON, storedJSON) {
		return ServingReport{}, errors.New("stored serving report does not match its recorded request observations")
	}
	return rebuilt, nil
}

func (bundle ServingBundle) JSON() ([]byte, error) {
	if _, err := bundle.Validate(); err != nil {
		return nil, err
	}
	data, err := json.MarshalIndent(bundle, "", "  ")
	if err != nil {
		return nil, err
	}
	if len(data) > maximumBundleBytes {
		return nil, fmt.Errorf("serving bundle exceeds %d bytes", maximumBundleBytes)
	}
	return append(data, '\n'), nil
}

func LoadServingBundle(path string) (ServingBundle, error) {
	data, err := boundedio.ReadFile(path, maximumBundleBytes)
	if err != nil {
		return ServingBundle{}, err
	}
	return decodeServingBundle(data)
}

func decodeServingBundle(data []byte) (ServingBundle, error) {
	if len(data) > maximumBundleBytes {
		return ServingBundle{}, fmt.Errorf("serving bundle exceeds %d bytes", maximumBundleBytes)
	}
	if err := strictjson.Validate(data); err != nil {
		return ServingBundle{}, fmt.Errorf("invalid serving bundle JSON: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var bundle ServingBundle
	if err := decoder.Decode(&bundle); err != nil {
		return ServingBundle{}, fmt.Errorf("invalid serving bundle JSON: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return ServingBundle{}, errors.New("content after serving bundle JSON")
	}
	if _, err := bundle.Validate(); err != nil {
		return ServingBundle{}, err
	}
	return bundle, nil
}
