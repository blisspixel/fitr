package calibration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/blisspixel/fitr/internal/boundedio"
)

var lineageDigest = regexp.MustCompile(`(?i)^sha256:[0-9a-f]{64}$`)

var (
	ErrGGUFNoBaseDigest       = errors.New("GGUF metadata does not name a base-revision digest")
	ErrGGUFNamedWithoutDigest = errors.New("GGUF metadata names a base model without a content digest")
)

// Closed GGUF keys that may carry a content digest of the exact base
// revision. Names, URLs, and Hugging Face ids are not accepted.
var ggufBaseDigestKeys = []string{
	"general.base_model.0.sha256",
	"general.base_model.0.digest",
	"general.source.sha256",
}

// ConversionArtifact is one blob named by a publisher conversion manifest.
// Role is optional: "base" must equal the manifest base revision, "derived"
// must not.
type ConversionArtifact struct {
	Digest string `json:"digest"`
	Quant  string `json:"quant,omitempty"`
	Role   string `json:"role,omitempty"`
}

// ConversionManifest is independently checkable derivation evidence. It names
// one base revision and the artifacts derived from it. Family names and
// operator belief are not fields.
type ConversionManifest struct {
	Schema       string               `json:"schema"`
	BaseRevision string               `json:"base_revision"`
	Artifacts    []ConversionArtifact `json:"artifacts"`
}

// GGUFLineageEvidence records the GGUF metadata observation that bound both
// artifacts to one base digest. A reviewer can inspect the claim; authenticity
// still requires an external trust policy on the pair.
type GGUFLineageEvidence struct {
	Key           string `json:"key"`
	ReferenceBase string `json:"reference_base_digest"`
	CandidateBase string `json:"candidate_base_digest"`
}

// LineageReceipt binds two runtime-bound artifact digests to one exact
// base-model revision. A pair signature seals this receipt; it cannot create
// one. Operator "same 8B family" claims never become a receipt.
type LineageReceipt struct {
	Schema          string               `json:"schema"`
	Method          string               `json:"method"`
	BaseRevision    string               `json:"base_revision"`
	ReferenceDigest string               `json:"reference_digest"`
	CandidateDigest string               `json:"candidate_digest"`
	EvidenceSHA256  string               `json:"evidence_sha256"`
	Conversion      *ConversionManifest  `json:"conversion,omitempty"`
	GGUF            *GGUFLineageEvidence `json:"gguf,omitempty"`
}

func normalizeDigest(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if value == "" {
		return "", errors.New("missing SHA-256 digest")
	}
	if !strings.Contains(value, ":") && len(value) == 64 {
		value = "sha256:" + value
	}
	if !lineageDigest.MatchString(value) {
		return "", fmt.Errorf("not a SHA-256 digest %q", value)
	}
	return value, nil
}

func evidenceSHA256(value any) (string, error) {
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte("fitr.lineage.evidence.v1\x00"), b...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func (a *ConversionArtifact) normalize() error {
	if a == nil {
		return errors.New("conversion artifact is missing")
	}
	digest, err := normalizeDigest(a.Digest)
	if err != nil {
		return err
	}
	a.Digest = digest
	a.Quant = strings.TrimSpace(a.Quant)
	a.Role = strings.ToLower(strings.TrimSpace(a.Role))
	switch a.Role {
	case "", "base", "derived":
		return nil
	default:
		return fmt.Errorf("unsupported conversion artifact role %q", a.Role)
	}
}

func (m *ConversionManifest) normalize() error {
	if m == nil {
		return errors.New("conversion manifest is missing")
	}
	m.Schema = strings.TrimSpace(m.Schema)
	base, err := normalizeDigest(m.BaseRevision)
	if err != nil {
		return fmt.Errorf("conversion base revision: %w", err)
	}
	m.BaseRevision = base
	if len(m.Artifacts) < 2 {
		return errors.New("conversion manifest must name at least two artifacts")
	}
	seen := map[string]bool{}
	for i := range m.Artifacts {
		if err := m.Artifacts[i].normalize(); err != nil {
			return fmt.Errorf("conversion artifact %d: %w", i+1, err)
		}
		digest := m.Artifacts[i].Digest
		if seen[digest] {
			return fmt.Errorf("conversion manifest repeats artifact %s", digest)
		}
		seen[digest] = true
		switch m.Artifacts[i].Role {
		case "base":
			if digest != m.BaseRevision {
				return errors.New("conversion artifact marked base does not match the base revision")
			}
		case "derived":
			if digest == m.BaseRevision {
				return errors.New("conversion artifact marked derived repeats the base revision")
			}
		}
	}
	return nil
}

func (m *ConversionManifest) Validate() error {
	if err := m.normalize(); err != nil {
		return err
	}
	if m.Schema != ConversionSchema {
		return fmt.Errorf("unsupported conversion manifest schema %q", m.Schema)
	}
	return nil
}

func (m ConversionManifest) binds(digest string) bool {
	for _, artifact := range m.Artifacts {
		if artifact.Digest == digest {
			return true
		}
	}
	return false
}

// ReadConversionManifest loads a publisher conversion document and rejects
// unknown fields or trailing JSON.
func ReadConversionManifest(path string) (ConversionManifest, error) {
	b, err := boundedio.ReadFile(path, maxCalibrationJSONBytes)
	if err != nil {
		return ConversionManifest{}, err
	}
	var m ConversionManifest
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&m); err != nil {
		return ConversionManifest{}, err
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return ConversionManifest{}, errors.New("content after the conversion manifest")
		}
		return ConversionManifest{}, err
	}
	if err := (&m).Validate(); err != nil {
		return ConversionManifest{}, err
	}
	return m, nil
}

// LineageFromConversion builds a receipt from a publisher manifest that names
// both pair artifacts and one base revision.
func LineageFromConversion(manifest ConversionManifest, referenceDigest, candidateDigest string) (LineageReceipt, error) {
	if err := (&manifest).Validate(); err != nil {
		return LineageReceipt{}, err
	}
	ref, err := normalizeDigest(referenceDigest)
	if err != nil {
		return LineageReceipt{}, fmt.Errorf("reference artifact: %w", err)
	}
	cand, err := normalizeDigest(candidateDigest)
	if err != nil {
		return LineageReceipt{}, fmt.Errorf("candidate artifact: %w", err)
	}
	if ref == cand {
		return LineageReceipt{}, errors.New("lineage requires two distinct artifact digests")
	}
	if !manifest.binds(ref) || !manifest.binds(cand) {
		return LineageReceipt{}, errors.New("conversion manifest does not name both pair artifacts")
	}
	copy := manifest
	sum, err := evidenceSHA256(copy)
	if err != nil {
		return LineageReceipt{}, err
	}
	return LineageReceipt{
		Schema: LineageSchema, Method: LineageConversion,
		BaseRevision: manifest.BaseRevision, ReferenceDigest: ref, CandidateDigest: cand,
		EvidenceSHA256: sum, Conversion: &copy,
	}, nil
}

func ggufMetadataString(kvs map[string]any, key string) string {
	v, ok := kvs[key]
	if !ok || v == nil {
		return ""
	}
	s, ok := v.(string)
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

func ggufNamedBaseWithoutDigest(kvs map[string]any) bool {
	for _, key := range []string{
		"general.base_model.0.name",
		"general.base_model.0.repo_url",
		"general.base_model.0.organization",
		"general.source.url",
	} {
		if ggufMetadataString(kvs, key) != "" {
			return true
		}
	}
	return false
}

func ggufBaseDigest(kvs map[string]any) (key, digest string, err error) {
	var foundKey, foundDigest string
	for _, candidate := range ggufBaseDigestKeys {
		raw := ggufMetadataString(kvs, candidate)
		if raw == "" {
			continue
		}
		normalized, normErr := normalizeDigest(raw)
		if normErr != nil {
			return "", "", fmt.Errorf("GGUF %s is not a SHA-256 digest", candidate)
		}
		if foundKey != "" && foundDigest != normalized {
			return "", "", fmt.Errorf("GGUF metadata names conflicting base digests in %s and %s", foundKey, candidate)
		}
		foundKey, foundDigest = candidate, normalized
	}
	if foundDigest == "" {
		if ggufNamedBaseWithoutDigest(kvs) {
			return "", "", ErrGGUFNamedWithoutDigest
		}
		return "", "", ErrGGUFNoBaseDigest
	}
	return foundKey, foundDigest, nil
}

// LineageFromGGUF builds a receipt when both artifacts' GGUF metadata name
// the same exact base-revision digest. File hashes must already equal the
// runtime-bound identity; this function does not hash files.
func LineageFromGGUF(referenceKVs, candidateKVs map[string]any, referenceDigest, candidateDigest string) (LineageReceipt, error) {
	ref, err := normalizeDigest(referenceDigest)
	if err != nil {
		return LineageReceipt{}, fmt.Errorf("reference artifact: %w", err)
	}
	cand, err := normalizeDigest(candidateDigest)
	if err != nil {
		return LineageReceipt{}, fmt.Errorf("candidate artifact: %w", err)
	}
	if ref == cand {
		return LineageReceipt{}, errors.New("lineage requires two distinct artifact digests")
	}
	refKey, refBase, err := ggufBaseDigest(referenceKVs)
	if err != nil {
		return LineageReceipt{}, fmt.Errorf("reference GGUF: %w", err)
	}
	candKey, candBase, err := ggufBaseDigest(candidateKVs)
	if err != nil {
		return LineageReceipt{}, fmt.Errorf("candidate GGUF: %w", err)
	}
	if refKey != candKey {
		return LineageReceipt{}, fmt.Errorf("GGUF base-digest keys differ: %s vs %s", refKey, candKey)
	}
	if refBase != candBase {
		return LineageReceipt{}, errors.New("GGUF metadata names different base-revision digests")
	}
	evidence := GGUFLineageEvidence{Key: refKey, ReferenceBase: refBase, CandidateBase: candBase}
	sum, err := evidenceSHA256(evidence)
	if err != nil {
		return LineageReceipt{}, err
	}
	return LineageReceipt{
		Schema: LineageSchema, Method: LineageGGUFDigest,
		BaseRevision: refBase, ReferenceDigest: ref, CandidateDigest: cand,
		EvidenceSHA256: sum, GGUF: &evidence,
	}, nil
}

func (l *LineageReceipt) normalize() error {
	if l == nil {
		return errors.New("lineage receipt is missing")
	}
	l.Schema = strings.TrimSpace(l.Schema)
	l.Method = strings.TrimSpace(l.Method)
	base, err := normalizeDigest(l.BaseRevision)
	if err != nil {
		return fmt.Errorf("lineage base revision: %w", err)
	}
	l.BaseRevision = base
	ref, err := normalizeDigest(l.ReferenceDigest)
	if err != nil {
		return fmt.Errorf("lineage reference digest: %w", err)
	}
	l.ReferenceDigest = ref
	cand, err := normalizeDigest(l.CandidateDigest)
	if err != nil {
		return fmt.Errorf("lineage candidate digest: %w", err)
	}
	l.CandidateDigest = cand
	sum, err := normalizeDigest(l.EvidenceSHA256)
	if err != nil {
		return fmt.Errorf("lineage evidence digest: %w", err)
	}
	l.EvidenceSHA256 = sum
	if l.Conversion != nil {
		if err := l.Conversion.normalize(); err != nil {
			return err
		}
	}
	if l.GGUF != nil {
		l.GGUF.Key = strings.TrimSpace(l.GGUF.Key)
		if l.GGUF.ReferenceBase, err = normalizeDigest(l.GGUF.ReferenceBase); err != nil {
			return fmt.Errorf("GGUF reference base: %w", err)
		}
		if l.GGUF.CandidateBase, err = normalizeDigest(l.GGUF.CandidateBase); err != nil {
			return fmt.Errorf("GGUF candidate base: %w", err)
		}
	}
	return nil
}

// Validate checks that the receipt is internally consistent and that its
// nested evidence still hashes to EvidenceSHA256. It does not consult an
// issuer or trust policy.
func (l *LineageReceipt) Validate() error {
	if err := l.normalize(); err != nil {
		return err
	}
	if l.Schema != LineageSchema {
		return fmt.Errorf("unsupported lineage schema %q", l.Schema)
	}
	if l.ReferenceDigest == l.CandidateDigest {
		return errors.New("lineage requires two distinct artifact digests")
	}
	switch l.Method {
	case LineageConversion:
		if l.Conversion == nil || l.GGUF != nil {
			return errors.New("conversion lineage must carry only a conversion manifest")
		}
		if err := l.Conversion.Validate(); err != nil {
			return err
		}
		if l.Conversion.BaseRevision != l.BaseRevision {
			return errors.New("conversion manifest base revision does not match the lineage receipt")
		}
		if !l.Conversion.binds(l.ReferenceDigest) || !l.Conversion.binds(l.CandidateDigest) {
			return errors.New("conversion manifest does not name both lineage artifacts")
		}
		sum, err := evidenceSHA256(*l.Conversion)
		if err != nil {
			return err
		}
		if sum != l.EvidenceSHA256 {
			return errors.New("lineage evidence digest does not match the conversion manifest")
		}
	case LineageGGUFDigest:
		if l.GGUF == nil || l.Conversion != nil {
			return errors.New("GGUF lineage must carry only GGUF metadata evidence")
		}
		if l.GGUF.Key == "" {
			return errors.New("GGUF lineage is missing the metadata key")
		}
		if l.GGUF.ReferenceBase != l.GGUF.CandidateBase || l.GGUF.ReferenceBase != l.BaseRevision {
			return errors.New("GGUF lineage does not bind both artifacts to one base digest")
		}
		sum, err := evidenceSHA256(*l.GGUF)
		if err != nil {
			return err
		}
		if sum != l.EvidenceSHA256 {
			return errors.New("lineage evidence digest does not match the GGUF evidence")
		}
	default:
		return fmt.Errorf("unsupported lineage method %q", l.Method)
	}
	return nil
}

// Bind reports whether this receipt names the two runtime-bound artifacts
// on a pair. A valid receipt from a different pair must not transfer.
func (l LineageReceipt) Bind(referenceDigest, candidateDigest string) error {
	if err := l.Validate(); err != nil {
		return err
	}
	ref, err := normalizeDigest(referenceDigest)
	if err != nil {
		return fmt.Errorf("pair reference digest: %w", err)
	}
	cand, err := normalizeDigest(candidateDigest)
	if err != nil {
		return fmt.Errorf("pair candidate digest: %w", err)
	}
	if l.ReferenceDigest != ref || l.CandidateDigest != cand {
		return errors.New("lineage receipt does not bind this pair's artifact digests")
	}
	return nil
}

// AttachLineage records a verified receipt on the pair. The pair remains
// unsigned; attaching lineage does not make it decision-grade.
func (r *PairReport) AttachLineage(l LineageReceipt) error {
	if r == nil {
		return errors.New("cannot attach lineage to a nil pair")
	}
	if err := l.Bind(r.Reference.ArtifactDigest, r.Candidate.ArtifactDigest); err != nil {
		return err
	}
	copy := l
	r.Lineage = &copy
	return nil
}
