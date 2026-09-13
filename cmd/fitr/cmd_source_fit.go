package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"math"
	"os"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/analysis"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/render"
	"github.com/blisspixel/fitr/internal/source"
)

type sourceServices struct {
	resolve func(context.Context, source.HFRequest) (source.Resolution, error)
	prefix  func(context.Context, string, string, string, int) ([]byte, source.PrefixObservation)
	detect  func(context.Context) device.Fingerprint
}

func defaultSourceServices() sourceServices {
	return sourceServices{resolve: source.ResolveHF, prefix: source.NewResolver(nil).FetchArtifactPrefixLimit,
		detect: func(ctx context.Context) device.Fingerprint { return device.Detect(ctx, nil) }}
}

type sourceFitFlags struct {
	enabled, screen, firstParty bool
	context, headerBytes        int
	budgetGB                    float64
	licenses, architectures     sourceFiles
}

func addSourceFitFlags(fs *flag.FlagSet) *sourceFitFlags {
	options := &sourceFitFlags{}
	fs.BoolVar(&options.enabled, "fit", false, "read bounded GGUF headers and project weights plus cache")
	fs.BoolVar(&options.screen, "screen", false, "apply publisher, license, architecture and component-ceiling gates")
	fs.IntVar(&options.context, "ctx", 0, "requested context (required for --screen; --fit defaults to artifact maximum)")
	fs.IntVar(&options.headerBytes, "header-bytes", source.MaxArtifactPrefixBytes, "explicit prefix byte bound per selected file, at most 8388608; no automatic retries")
	fs.Float64Var(&options.budgetGB, "fit-budget-gb", 0, "operator ceiling in GiB for projected weights plus cache, excluding runtime overhead")
	fs.BoolVar(&options.firstParty, "require-first-party", false, "require every declared base model to share the repository author")
	fs.Var(&options.licenses, "allow-license", "accepted declared license identifier; repeat for alternatives")
	fs.Var(&options.architectures, "allow-architecture", "accepted GGUF architecture identifier; does not establish runtime support")
	return options
}

func (options sourceFitFlags) policy() analysis.SourceScreenPolicy {
	return analysis.SourceScreenPolicy{RequireFirstParty: options.firstParty,
		AllowedLicenses: []string(options.licenses), AllowedArchitectures: []string(options.architectures),
		Context: options.context, ComponentCeilingBytes: int64(options.budgetGB * advise.GiB)}
}

func (options sourceFitFlags) validate() error {
	if options.headerBytes <= 0 || options.headerBytes > source.MaxScreeningPrefixBytes {
		return errors.New("--header-bytes must be between 1 and 8388608")
	}
	if options.context < 0 || options.context > 1<<30 || math.IsNaN(options.budgetGB) || math.IsInf(options.budgetGB, 0) ||
		options.budgetGB < 0 || options.budgetGB > 1<<30 || (options.budgetGB > 0 && options.budgetGB*advise.GiB < 1) {
		return errors.New("source projection needs a bounded nonnegative context and memory ceiling")
	}
	if options.screen {
		return options.policy().Validate()
	}
	if options.firstParty || len(options.licenses) != 0 || len(options.architectures) != 0 {
		return errors.New("publisher, license and architecture policies require --screen")
	}
	if !options.enabled && (options.context != 0 || options.budgetGB != 0 || options.headerBytes != source.MaxArtifactPrefixBytes) {
		return errors.New("--ctx, --fit-budget-gb and --header-bytes require --fit or --screen")
	}
	return nil
}

// This output is a fresh analysis, not an extension of the source receipt's
// integrity seal. The source digest, public header provenance and prefix hashes
// make its inputs inspectable without storing raw headers or signed URLs.
type sourceFitOutput struct {
	Schema     string                  `json:"schema"`
	Resolution source.Resolution       `json:"resolution"`
	Screen     *analysis.SourceScreen  `json:"screen,omitempty"`
	Projection *advise.SourceFitReport `json:"projection,omitempty"`
	Headers    []sourceHeaderRead      `json:"header_reads"`
}

type sourceHeaderRead = analysis.SourceHeaderRead

func writeResolvedFit(ctx context.Context, resolution source.Resolution, options *sourceFitFlags, mode string, services sourceServices) int {
	output := sourceFitOutput{Schema: "fitr.source.screening.v1", Resolution: resolution, Headers: []sourceHeaderRead{}}
	if options.screen {
		screen, err := analysis.AnalyzeSourceScreen(resolution, options.policy(), analysis.SourceScreenFacts{})
		if err != nil {
			return sourceFailure(err)
		}
		output.Screen = &screen
	}
	if sourceNeedsHeaders(output, options) && ctx.Err() == nil {
		collectSourceFit(ctx, &output, options, services)
	}
	if output.Screen != nil && output.Projection != nil {
		facts := sourceScreenFacts(*output.Projection)
		screen, err := analysis.AnalyzeSourceScreen(resolution, options.policy(), facts)
		if err != nil {
			return sourceFailure(err)
		}
		output.Screen = &screen
	}
	if err := output.write(mode); err != nil {
		return sourceFailure(err)
	}
	if ctx.Err() != nil {
		return exitInterrupt
	}
	if output.Screen != nil && output.Screen.State == "clear" {
		return exitOK
	}
	if output.Screen == nil && output.Projection != nil && output.Projection.ProjectionStatus == "within_ceiling" {
		return exitOK
	}
	return exitUnresolved
}

func sourceNeedsHeaders(output sourceFitOutput, options *sourceFitFlags) bool {
	if output.Resolution.State != "resolved" {
		return false
	}
	return output.Screen == nil || (output.Screen.FirstProblem == "architecture" && len(options.architectures) > 0)
}

func collectSourceFit(ctx context.Context, output *sourceFitOutput, options *sourceFitFlags, services sourceServices) {
	request := advise.SourceFitRequest{Context: options.context,
		CapacityBytes: int64(options.budgetGB * advise.GiB), CapacitySource: "operator component ceiling"}
	if request.CapacityBytes == 0 {
		fingerprint := services.detect(ctx)
		if fingerprint.VRAMGb > 0 && fingerprint.VRAMGb <= 1<<30 && !math.IsInf(fingerprint.VRAMGb, 0) {
			request.CapacityBytes = int64(fingerprint.VRAMGb * advise.GiB)
		}
		request.CapacitySource = fingerprint.VRAMSource + "; addressable capacity, not a safe runtime budget"
	}
	var headers []advise.SourceHeader
	for _, file := range output.Resolution.Files {
		if ctx.Err() != nil {
			break
		}
		body, observation := services.prefix(ctx, output.Resolution.ResolvedRepo, output.Resolution.ResolvedCommit, file.Path, options.headerBytes)
		read := sourceHeaderRead{Path: file.Path, Observation: observation}
		if body != nil {
			read.SHA256 = fmt.Sprintf("sha256:%x", sha256.Sum256(body))
		}
		output.Headers = append(output.Headers, read)
		headers = append(headers, advise.SourceHeader{Path: file.Path, Body: body, Observation: observation})
	}
	projection := advise.ProjectSourceFit(output.Resolution, headers, request)
	output.Projection = &projection
}

func sourceScreenFacts(projection advise.SourceFitReport) analysis.SourceScreenFacts {
	facts := analysis.SourceScreenFacts{SourceSHA256: projection.SourceSHA256, Context: projection.Context,
		ArchitectureStatus: projection.ArchitectureStatus,
		ArchitectureReason: projection.ArchitectureReason, ProjectionStatus: projection.ProjectionStatus,
		ProjectionReason: projection.ProjectionReason}
	if projection.Shape != nil {
		facts.Architecture = projection.Shape.Name
	}
	if projection.CapacityBytes != nil {
		facts.CeilingBytes = *projection.CapacityBytes
	}
	return facts
}

func (output sourceFitOutput) write(mode string) error {
	var report bytes.Buffer
	switch render.Resolve(mode) {
	case "none":
		return nil
	case "json":
		encoder := json.NewEncoder(&report)
		encoder.SetIndent("", "  ")
		if err := encoder.Encode(output); err != nil {
			return err
		}
	default:
		render.WriteSourceResolution(&report, output.Resolution, mode)
		if output.Screen != nil {
			render.WriteSourceScreen(&report, *output.Screen)
		}
		render.WriteSourceProjection(&report, output.Projection, output.Headers)
	}
	_, err := os.Stdout.Write(report.Bytes())
	return err
}
