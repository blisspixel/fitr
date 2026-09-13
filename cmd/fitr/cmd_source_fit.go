package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/blisspixel/fitr/internal/advise"
	"github.com/blisspixel/fitr/internal/device"
	"github.com/blisspixel/fitr/internal/source"
)

// projectResolvedFit answers whether a candidate would fit before anyone spends
// the bandwidth to find out.
//
// The declared size gives the weights and the artifact's opening bytes give the
// architecture, so the same arithmetic that serves an installed model serves a
// remote one. What differs is provenance, and the output says so: the size is
// the provider's declaration rather than a file fitr hashed, and the header
// came from whichever host the provider redirected to. Neither is a local
// artifact observation and neither promotes the candidate to measured.
func projectResolvedFit(ctx context.Context, resolution source.Resolution, ctxSize int, mode string) {
	if resolution.ResolvedCommit == "" {
		fmt.Fprintln(os.Stderr, "  fit  skipped: the resolution pinned no commit to read from")
		return
	}
	fingerprint := device.Detect(ctx, nil)
	resolver := source.NewResolver(nil)
	for _, file := range resolution.Files {
		if file.SizeBytes == nil || *file.SizeBytes <= 0 {
			continue
		}
		body, observation := resolver.FetchArtifactPrefix(ctx,
			resolution.ResolvedRepo, resolution.ResolvedCommit, file.Path)
		fmt.Fprintf(os.Stderr, "  prefix   %s  %d byte(s) from %s via %d redirect(s): %s\n",
			terminalText(file.Path), observation.Bytes, terminalText(observation.Host),
			observation.Redirects, terminalText(observation.Outcome))
		if body == nil {
			continue
		}
		kvs, err := advise.ReadMetadataPrefix(bytes.NewReader(body))
		if len(kvs) == 0 {
			fmt.Fprintf(os.Stderr, "  fit      %s: the opening bytes are not a readable GGUF header (%v)\n",
				terminalText(file.Path), err)
			continue
		}
		arch := advise.ArchFromKVs(kvs)
		truncated := errors.Is(err, advise.ErrMetadataTruncated)
		report := advise.Evaluate(advise.Input{
			Model: resolution.ResolvedRepo + "/" + file.Path,
			// The provider's declared byte count, not a hash of local bytes.
			WeightsB: *file.SizeBytes,
			HaveGB:   fingerprint.VRAMGb, HaveSrc: fingerprint.VRAMSource,
			Ctx: ctxSize, Arch: arch,
			Source: "provider-declared size; header read from " + observation.Host,
		})
		if truncated {
			// A bounded prefix stops mid-file by design, which is how a 15 GB
			// candidate costs 32 KiB to size. The cost is that absence proves
			// nothing here: the keys that would have refused this projection,
			// a sliding window or a recurrent layer count, are exactly the
			// ones that could sit past the boundary. Say so rather than let a
			// confident verdict stand on keys nobody read.
			report.Gaps = append(report.Gaps,
				"the header was read as a bounded prefix and ended before the file did, "+
					"so a key that would change this projection may sit past the bytes read")
		}
		if mode == "json" {
			enc := json.NewEncoder(os.Stdout)
			enc.SetIndent("", "  ")
			if err := enc.Encode(report); err != nil {
				fmt.Fprintln(os.Stderr, "  fit      could not encode:", err)
			}
			continue
		}
		advise.Write(os.Stdout, report)
	}
}
