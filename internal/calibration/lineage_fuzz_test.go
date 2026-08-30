package calibration

import (
	"strings"
	"testing"
)

const fuzzDigestA = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
const fuzzDigestB = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
const fuzzDigestC = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"

// A conversion manifest is a publisher document handed to fitr by the user
// (`--lineage conversion.json`). It decides whether two quants may be compared
// as the same base, which is the claim the whole paired-quant protocol rests
// on. The parser is hardened; this covers the consumers behind it.
//
// The invariant: a manifest that validates must produce a receipt that
// validates and binds only the artifacts it actually names.
func FuzzConversionManifest(f *testing.F) {
	f.Add(`{"schema":"` + ConversionSchema + `","base_revision":"` + fuzzDigestA + `","artifacts":[` +
		`{"digest":"` + fuzzDigestA + `","role":"base","quant":"F16"},` +
		`{"digest":"` + fuzzDigestB + `","role":"derived","quant":"Q4_K_M"}]}`)
	f.Add(`{"schema":"` + ConversionSchema + `","base_revision":"` + fuzzDigestA + `","artifacts":[` +
		`{"digest":"` + fuzzDigestA + `","role":"base"},{"digest":"` + fuzzDigestA + `","role":"derived"}]}`)
	f.Add(`{"schema":"nope","base_revision":"x","artifacts":[]}`)
	f.Add(`{"artifacts":[{"digest":""},{"digest":""}]}`)
	f.Add(`{}`)
	f.Add(``)

	f.Fuzz(func(t *testing.T, body string) {
		m, err := decodeConversionManifest([]byte(body))
		if err != nil {
			return
		}
		assertAcceptedConversionManifest(t, m)
	})
}

func assertAcceptedConversionManifest(t *testing.T, m ConversionManifest) {
	t.Helper()
	// Accepted. Every invariant the protocol relies on must now hold.
	if m.Schema != ConversionSchema {
		t.Fatalf("accepted a manifest with schema %q", m.Schema)
	}
	if len(m.Artifacts) < 2 {
		t.Fatalf("accepted a manifest naming %d artifacts", len(m.Artifacts))
	}
	named := assertValidConversionArtifacts(t, m)
	assertConversionReceipt(t, m, named)
}

func assertValidConversionArtifacts(t *testing.T, m ConversionManifest) []string {
	t.Helper()
	seen := map[string]bool{}
	named := make([]string, 0, len(m.Artifacts))
	for _, artifact := range m.Artifacts {
		if artifact.Digest == "" {
			t.Fatalf("accepted an artifact with no digest: %+v", m)
		}
		if seen[artifact.Digest] {
			t.Fatalf("accepted a manifest repeating artifact %s", artifact.Digest)
		}
		seen[artifact.Digest] = true
		if strings.TrimSpace(artifact.Digest) != artifact.Digest {
			t.Fatalf("digest %q was not normalized", artifact.Digest)
		}
		named = append(named, artifact.Digest)
	}
	return named
}

func assertConversionReceipt(t *testing.T, m ConversionManifest, named []string) {
	t.Helper()
	// A receipt may only be built for two distinct artifacts the manifest
	// names, and it must never bind anything it does not name.
	receipt, err := LineageFromConversion(m, named[0], named[1])
	if err != nil {
		return
	}
	if err := receipt.Validate(); err != nil {
		t.Fatalf("a receipt built from an accepted manifest does not validate: %v", err)
	}
	if err := receipt.Bind(named[0], named[1]); err != nil {
		t.Fatalf("receipt does not bind the pair it was built from: %v", err)
	}
	if err := receipt.Bind(named[0], fuzzDigestC); err == nil {
		t.Fatalf("receipt bound an artifact the manifest never named: %+v", receipt)
	}
	if receipt.EvidenceSHA256 == "" {
		t.Fatalf("receipt carries no evidence hash: %+v", receipt)
	}
	// Same artifact twice is not a pair.
	if _, err := LineageFromConversion(m, named[0], named[0]); err == nil {
		t.Fatal("an artifact was accepted as its own comparison pair")
	}
}
