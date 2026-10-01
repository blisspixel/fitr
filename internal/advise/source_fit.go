package advise

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strings"

	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/llm"
	"github.com/blisspixel/fitr/internal/source"
)

// SourceHeader binds the bytes supplied to the projection to one selected
// filename and its bounded transport observation. Raw bytes are not exported.
type SourceHeader struct {
	Path        string
	Body        []byte
	Observation source.PrefixObservation
}

// SourceFitRequest is an operator's component ceiling, not a claim about free
// or resident memory. Context zero uses the artifact's declared maximum.
type SourceFitRequest struct {
	Context        int
	CapacityBytes  int64
	CapacitySource string
	RuntimeProfile *llm.RuntimeSupportProfile
	ComponentPlan  *source.ComponentPlan
}

// SourceFitReport describes only declared weights plus modeled cache. Its
// ceiling comparison cannot establish runtime support or a measured fit.
type SourceFitReport = analysis.SourceFitReport

// ProjectSourceFit reuses the local cache arithmetic after validating the
// complete selected group. A readable prefix does not prove absent layout keys:
// those keys can follow a large vocabulary, so truncation cannot clear a gate.
func ProjectSourceFit(resolution source.Resolution, headers []SourceHeader, request SourceFitRequest) SourceFitReport {
	report := SourceFitReport{
		Schema: "fitr.source.fit.v1", ArchitectureStatus: "unresolved", ProjectionStatus: "unresolved",
		RuntimeStatus: "unmeasured", Files: []string{},
		Gaps: []string{"provider-declared sizes and header bytes have not been verified against whole-file hashes",
			"runtime support, placement and resident allocation are unmeasured",
			"runtime buffers and required companions are excluded; dependency closure is unverified",
			"cache projection assumes f16 KV elements"},
	}
	if err := resolution.Validate(); err != nil {
		return unresolvedSourceFit(report, "the source receipt did not validate")
	}
	report.SourceSHA256 = resolution.ResolutionSHA256
	if request.Context > 0 {
		report.Context = request.Context
	}
	if request.CapacityBytes > 0 && request.CapacitySource != "" {
		report.CapacityBytes, report.CapacitySource = &request.CapacityBytes, request.CapacitySource
	}
	if resolution.State != "resolved" {
		return unresolvedSourceFit(report, "selected source metadata is unresolved")
	}
	files, reason := source.SelectedGGUFGroup(resolution.Files)
	if reason != "" {
		return unresolvedSourceFit(report, reason)
	}
	for _, file := range files {
		report.Files = append(report.Files, file.Path)
	}
	arch, weights, reason := sourceGroupArchitecture(files, headers)
	if arch.Name != "" {
		report.Shape = &ArchShape{Name: arch.Name}
	}
	if reason != "" {
		return unresolvedSourceFit(report, reason)
	}
	report.Shape, report.WeightsBytes = arch.Shape(), &weights
	if !arch.KVReady() {
		reason, _ = arch.UnsizableReason()
		return unresolvedSourceFit(report, reason)
	}
	report.ArchitectureStatus = "available"
	report.ArchitectureReason = "complete selected GGUF metadata supports the cache projection; runtime support is unmeasured"

	requiredCompanions, blocked := applyRuntimeProfile(&report, request.RuntimeProfile, arch.Name)
	if blocked {
		return report
	}

	plan := resolveComponentPlan(resolution, request, arch.Name, requiredCompanions)
	if !applyComponentPlan(&report, plan, &weights) {
		return report
	}

	return compareSourceComponents(report, arch, weights, request)
}

func applyRuntimeProfile(report *SourceFitReport, profile *llm.RuntimeSupportProfile, archName string) ([]string, bool) {
	if profile == nil {
		return nil, false
	}
	report.RuntimeProfile = fmt.Sprintf("%s %s", profile.Runtime, profile.Version)
	requiredCompanions := profile.RequiredCompanionsFor(archName)
	_, status := profile.CheckArchitecture(archName)
	switch status {
	case llm.ArchitectureSupported:
		report.RuntimeStatus = "profile_supported"
		report.RuntimeReason = fmt.Sprintf("runtime profile %s %s declares support for architecture %s; behavioral execution remains unmeasured",
			profile.Runtime, profile.Version, archName)
	case llm.ArchitectureUnsupported:
		report.RuntimeStatus = "unsupported"
		report.RuntimeReason = fmt.Sprintf("runtime profile %s %s explicitly does not support architecture %s",
			profile.Runtime, profile.Version, archName)
		report.ProjectionStatus = "blocked"
		report.ProjectionReason = report.RuntimeReason
		return requiredCompanions, true
	case llm.ArchitectureUnresolved:
		report.RuntimeStatus = "unresolved"
		report.RuntimeReason = fmt.Sprintf("architecture %s is unlisted in runtime profile %s %s",
			archName, profile.Runtime, profile.Version)
	}
	return requiredCompanions, false
}

func resolveComponentPlan(resolution source.Resolution, request SourceFitRequest, archName string, requiredCompanions []string) *source.ComponentPlan {
	if request.ComponentPlan != nil {
		return request.ComponentPlan
	}
	if resolution.ComponentPlan != nil {
		return resolution.ComponentPlan
	}
	if request.RuntimeProfile != nil || len(requiredCompanions) > 0 {
		plan, err := source.BuildComponentPlan(resolution, source.BuildComponentPlanOptions{
			Architecture:       archName,
			RequiredCompanions: requiredCompanions,
		})
		if err == nil {
			return plan
		}
	}
	return nil
}

func applyComponentPlan(report *SourceFitReport, plan *source.ComponentPlan, weights *int64) bool {
	if plan == nil {
		return true
	}
	report.ComponentPlan = plan
	switch plan.Status {
	case source.PlanBlocked:
		report.ProjectionStatus = "blocked"
		report.ProjectionReason = "component plan is blocked: " + plan.Reason
		return false
	case source.PlanUnresolved:
		report.ProjectionStatus = "unresolved"
		report.ProjectionReason = "component plan is unresolved: " + plan.Reason
		report.Gaps = append(report.Gaps, plan.Gaps...)
		slices.Sort(report.Gaps)
		report.Gaps = slices.Compact(report.Gaps)
		return false
	case source.PlanComplete:
		*weights = plan.TotalRequiredBytes
		report.WeightsBytes = weights
		for i, gap := range report.Gaps {
			if gap == "runtime buffers and required companions are excluded; dependency closure is unverified" {
				report.Gaps[i] = "runtime buffers and placement are unmeasured; required companions are planned"
			}
		}
		return true
	}
	return true
}

// RederiveSourceFit re-computes the component projection from validated header
// observations and screening policy without network access.
func RederiveSourceFit(resolution source.Resolution) *SourceFitReport {
	if resolution.HeaderObservations == nil {
		return nil
	}
	report := initialRederivedReport(resolution)
	obs := resolution.HeaderObservations
	if obs.Shape == nil || obs.WeightsBytes == nil || obs.ArchitectureStatus != "available" {
		if report.ProjectionReason == "" {
			report.ProjectionReason = "GGUF metadata was incomplete or unreadable; cache projection is unavailable"
		}
		return report
	}
	return evaluateRederivedProjection(report, obs, resolution.ScreenPolicy, resolution.ComponentPlan)
}

func initialRederivedReport(resolution source.Resolution) *SourceFitReport {
	obs := resolution.HeaderObservations
	files := make([]string, 0, len(resolution.Files))
	for _, file := range resolution.Files {
		files = append(files, file.Path)
	}
	return &SourceFitReport{
		Schema:             "fitr.source.fit.v1",
		SourceSHA256:       resolution.ResolutionSHA256,
		ArchitectureStatus: obs.ArchitectureStatus,
		ArchitectureReason: obs.ArchitectureReason,
		ProjectionStatus:   "unresolved",
		ProjectionReason:   obs.ArchitectureReason,
		RuntimeStatus:      "unmeasured",
		Files:              files,
		Gaps: []string{
			"provider-declared sizes and header bytes have not been verified against whole-file hashes",
			"runtime support, placement and resident allocation are unmeasured",
			"runtime buffers and required companions are excluded; dependency closure is unverified",
			"cache projection assumes f16 KV elements",
		},
		WeightsBytes: obs.WeightsBytes,
		Shape:        obs.Shape,
	}
}

func evaluateRederivedProjection(report *SourceFitReport, obs *source.HeaderObservations, policy *source.ScreenPolicy, plan *source.ComponentPlan) *SourceFitReport {
	context, ceiling, errReason := deriveContextAndCeiling(obs.Shape, policy)
	if errReason != "" {
		report.ProjectionReason = errReason
		return report
	}
	report.Context = context
	cache, errReason := projectShapeCache(obs.Shape, context)
	if errReason != "" {
		report.ProjectionReason = errReason
		return report
	}
	weights := *obs.WeightsBytes
	if !applyComponentPlan(report, plan, &weights) {
		return report
	}
	components, ok := addInt64(weights, cache)
	if !ok {
		report.ProjectionReason = "declared weights plus projected cache overflow the supported byte range"
		return report
	}
	report.CacheBytes, report.ComponentBytes = &cache, &components
	if ceiling <= 0 {
		report.ProjectionReason = "an explicit positive operator ceiling is required for the component comparison"
		return report
	}
	report.CapacityBytes = &ceiling
	report.CapacitySource = "operator component ceiling"
	report.ProjectionStatus = "within_ceiling"
	if components > ceiling {
		report.ProjectionStatus = "exceeds_ceiling"
	}
	report.ProjectionReason = fmt.Sprintf("declared weights plus f16 cache at %d tokens are %s; runtime allocation is unmeasured",
		context, report.ProjectionStatus)
	return report
}

func deriveContextAndCeiling(shape *source.ArchitectureShape, policy *source.ScreenPolicy) (int, int64, string) {
	context := 0
	var ceiling int64
	if policy != nil {
		context = policy.Context
		ceiling = policy.ComponentCeilingBytes
	}
	if context == 0 {
		context = shape.MaxCtx
	}
	if context <= 0 {
		return 0, 0, "a positive requested context or declared model maximum is required"
	}
	if shape.MaxCtx > 0 && context > shape.MaxCtx {
		return 0, 0, "requested context exceeds the artifact's declared maximum"
	}
	return context, ceiling, ""
}

func projectShapeCache(shape *source.ArchitectureShape, context int) (int64, string) {
	if !shape.KVSizable {
		reason := shape.UnsizableReason
		if reason == "" {
			reason = "the artifact is not sizable"
		}
		return 0, reason
	}
	perToken := float64(shape.KVBytesPerToken)
	projected := perToken*float64(context) + float64(shape.FixedCacheBytes)
	if projected <= 0 || math.IsNaN(projected) || math.IsInf(projected, 0) || projected >= float64(math.MaxInt64) {
		return 0, "the requested cache projection exceeds the supported arithmetic range"
	}
	return int64(math.Ceil(projected)), ""
}

func unresolvedSourceFit(report SourceFitReport, reason string) SourceFitReport {
	report.ArchitectureReason, report.ProjectionReason = reason, reason
	return report
}

func sourceGroupArchitecture(files []source.FileMetadata, headers []SourceHeader) (Arch, int64, string) {
	if len(headers) != len(files) {
		return Arch{}, 0, "each selected file requires exactly one bounded header observation"
	}
	byPath := make(map[string]SourceHeader, len(headers))
	for _, header := range headers {
		if _, exists := byPath[header.Path]; exists {
			return Arch{}, 0, "duplicate header observations cannot describe one selected artifact"
		}
		byPath[header.Path] = header
	}
	var firstArch Arch
	var firstMetadata map[string]any
	var total int64
	for index, file := range files {
		header, found := byPath[file.Path]
		if !found || !validSourceHeader(header) {
			return firstArch, 0, "a selected file has no complete bounded prefix observation"
		}
		if (header.Observation.TotalBytes != nil && *header.Observation.TotalBytes != *file.SizeBytes) ||
			int64(len(header.Body)) > *file.SizeBytes {
			return firstArch, 0, "the artifact response size contradicts the pinned file metadata"
		}
		arch, kvs, reason := sourceHeaderArchitecture(header)
		if reason != "" {
			return arch, 0, reason
		}
		if index == 0 {
			if arch.Name == "" {
				return arch, 0, "the complete first header does not declare a GGUF architecture"
			}
			firstArch, firstMetadata = arch, kvs
		} else if !sourceShardArchitectureMatches(firstArch.Name, firstMetadata, kvs) {
			return firstArch, 0, "selected shard headers disagree about the model architecture"
		}
		if !sourceShardMetadata(kvs, index, len(files)) {
			return firstArch, 0, "GGUF split metadata does not establish the complete selected shard group"
		}
		var ok bool
		total, ok = addInt64(total, *file.SizeBytes)
		if !ok {
			return firstArch, 0, "declared weight sizes overflow the supported byte range"
		}
	}
	return firstArch, total, ""
}

func validSourceHeader(header SourceHeader) bool {
	bound := header.Observation.RequestedBytes
	if bound == 0 {
		bound = source.MaxArtifactPrefixBytes
	}
	return bound > 0 && bound <= source.MaxScreeningPrefixBytes &&
		header.Observation.Outcome == "resolved" && len(header.Body) > 0 &&
		len(header.Body) <= bound && header.Observation.Bytes == len(header.Body)
}

func sourceHeaderArchitecture(header SourceHeader) (Arch, map[string]any, string) {
	kvs, err := ReadMetadataPrefix(bytes.NewReader(header.Body))
	arch := ArchFromKVs(kvs)
	if errors.Is(err, ErrMetadataTruncated) {
		return arch, nil, "the bounded prefix ended before metadata completed; unread keys may change the cache layout"
	}
	if err != nil {
		return Arch{}, nil, "the selected file does not contain readable GGUF metadata"
	}
	return arch, kvs, ""
}

func sourceShardArchitectureMatches(architecture string, firstMetadata, metadata map[string]any) bool {
	// GGUFWriter.add_shard_kv_data in llama.cpp
	// 4a89937354190cef5a97baf8eeb17336105eb72d writes model metadata only in
	// shard zero. Later shards need no duplicate architecture, but any fields
	// they do declare must agree with the authoritative first header.
	for key, value := range metadata {
		if key == "general.architecture" || strings.HasPrefix(key, architecture+".") {
			if !reflect.DeepEqual(firstMetadata[key], value) {
				return false
			}
		}
	}
	return true
}

func sourceShardMetadata(kvs map[string]any, index, count int) bool {
	// These wire keys and zero-based split.no were checked against llama.cpp
	// 4a89937354190cef5a97baf8eeb17336105eb72d GGUFWriter.add_shard_kv_data
	// and gguf-py/gguf/constants.py.
	number, hasNumber := kvs["split.no"]
	total, hasTotal := kvs["split.count"]
	if count == 1 && !hasNumber && !hasTotal {
		return true
	}
	n, numberOK := number.(uint64)
	c, countOK := total.(uint64)
	return numberOK && countOK && n == uint64(index) && c == uint64(count)
}

func compareSourceComponents(report SourceFitReport, arch Arch, weights int64, request SourceFitRequest) SourceFitReport {
	contextTokens := request.Context
	if contextTokens == 0 {
		contextTokens = arch.MaxCtx
	}
	if contextTokens <= 0 {
		report.ProjectionReason = "a positive requested context or declared model maximum is required"
		return report
	}
	report.Context = contextTokens
	if arch.MaxCtx > 0 && contextTokens > arch.MaxCtx {
		report.ProjectionReason = "requested context exceeds the artifact's declared maximum"
		return report
	}
	cache, ok := ProjectKVBytes(arch, contextTokens, 2)
	if !ok {
		report.ProjectionReason = "the requested cache projection exceeds the supported arithmetic range"
		return report
	}
	components, ok := addInt64(weights, cache)
	if !ok {
		report.ProjectionReason = "declared weights plus projected cache overflow the supported byte range"
		return report
	}
	report.CacheBytes, report.ComponentBytes = &cache, &components
	if request.CapacityBytes <= 0 || request.CapacitySource == "" {
		report.ProjectionReason = "an explicit positive operator ceiling is required for the component comparison"
		return report
	}
	report.CapacityBytes, report.CapacitySource = &request.CapacityBytes, request.CapacitySource
	report.ProjectionStatus = "within_ceiling"
	if components > request.CapacityBytes {
		report.ProjectionStatus = "exceeds_ceiling"
	}
	report.ProjectionReason = fmt.Sprintf("declared weights plus f16 cache at %d tokens are %s; runtime allocation is unmeasured",
		contextTokens, report.ProjectionStatus)
	return report
}
