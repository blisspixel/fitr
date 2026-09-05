package discovery

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"reflect"
	"regexp"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/source"
	"github.com/blisspixel/fitr/internal/strictjson"
)

const (
	SourceAttachmentSchema         = "fitr.discovery.source.v1"
	SourcePlanSchema               = "fitr.discovery.plan.v2"
	MaxSourcesPerIdea              = 4
	MaxSourceStoreBytes      int64 = 256 << 20
	maxSourceAttachmentBytes       = 2 << 20
	maxSourceAttachments           = maximumIdeas * MaxSourcesPerIdea
)

var sourceIDPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

// SourceAttachment associates an operator-selected receipt with an unmeasured
// idea. It does not prove that the original source recommended those files.
type SourceAttachment struct {
	Schema           string            `json:"schema"`
	AttachmentSHA256 string            `json:"attachment_sha256"`
	IdeaID           string            `json:"idea_id"`
	ResolutionSHA256 string            `json:"resolution_sha256"`
	Relation         string            `json:"relation"`
	AttachedAt       string            `json:"attached_at"`
	Resolution       source.Resolution `json:"resolution"`
}

type SourceSummary struct {
	ResolutionSHA256 string                `json:"resolution_sha256"`
	AttachedAt       string                `json:"attached_at"`
	Relation         string                `json:"relation"`
	MetadataState    string                `json:"metadata_state"`
	RepoID           string                `json:"repo_id,omitempty"`
	Commit           string                `json:"commit,omitempty"`
	Files            []source.FileMetadata `json:"files"`
	DependencyCount  int                   `json:"dependency_count"`
}

func (attachment SourceAttachment) Validate() error {
	digest, err := attachment.Digest()
	if err != nil {
		return err
	}
	if digest != attachment.AttachmentSHA256 {
		return errors.New("discovery source association digest mismatch")
	}
	return nil
}

func (attachment SourceAttachment) Digest() (string, error) {
	if attachment.Schema != SourceAttachmentSchema || !sourceIDPattern.MatchString(attachment.IdeaID) ||
		attachment.Relation != "operator_association" || attachment.ResolutionSHA256 != attachment.Resolution.ResolutionSHA256 {
		return "", errors.New("invalid discovery source association")
	}
	when, err := time.Parse(time.RFC3339Nano, attachment.AttachedAt)
	if err != nil || when.IsZero() || when.UTC().Format(time.RFC3339Nano) != attachment.AttachedAt {
		return "", errors.New("invalid discovery association timestamp")
	}
	if err := attachment.Resolution.Validate(); err != nil {
		return "", err
	}
	attachment.AttachmentSHA256 = ""
	data, err := json.Marshal(attachment)
	if err != nil {
		return "", err
	}
	if len(data)+71 > maxSourceAttachmentBytes {
		return "", errors.New("discovery source association exceeds size bound")
	}
	return sourceHash(append([]byte(SourceAttachmentSchema+"\x00"), data...)), nil
}

func sourceHash(data []byte) string {
	sum := sha256.Sum256(data)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func (attachment SourceAttachment) summary() SourceSummary {
	repoID := attachment.Resolution.ResolvedRepo
	if repoID == "" {
		repoID = attachment.Resolution.Request.RepoID
	}
	return SourceSummary{ResolutionSHA256: attachment.ResolutionSHA256, AttachedAt: attachment.AttachedAt,
		Relation: attachment.Relation, MetadataState: attachment.Resolution.State, RepoID: repoID,
		Commit: attachment.Resolution.ResolvedCommit, Files: attachment.Resolution.Files, DependencyCount: len(attachment.Resolution.Dependencies)}
}

func decodeSourceAttachment(data []byte) (SourceAttachment, error) {
	var attachment SourceAttachment
	if len(data) > maxSourceAttachmentBytes || strictjson.Validate(data) != nil {
		return attachment, errors.New("invalid discovery source JSON")
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&attachment); err != nil {
		return attachment, err
	}
	// A round trip detects case-folded keys accepted by encoding/json, including
	// keys in the embedded receipt. Unknown fields were already rejected above.
	canonical, err := json.Marshal(attachment)
	if err != nil {
		return attachment, err
	}
	var originalTree, canonicalTree any
	if err := json.Unmarshal(data, &originalTree); err != nil {
		return attachment, err
	}
	if err := json.Unmarshal(canonical, &canonicalTree); err != nil {
		return attachment, err
	}
	if !reflect.DeepEqual(originalTree, canonicalTree) {
		return attachment, errors.New("noncanonical discovery source fields")
	}
	return attachment, attachment.Validate()
}

func sourceDigestFilename(digest string) (string, error) {
	hexDigest, ok := strings.CutPrefix(digest, "sha256:")
	if !ok || !sourceIDPattern.MatchString(hexDigest) {
		return "", errors.New("a full source resolution digest is required")
	}
	return hexDigest + ".json", nil
}
