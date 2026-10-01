package discovery

import (
	"errors"
	"path/filepath"
	"strings"

	"github.com/blisspixel/fitr/internal/artifact"
	"github.com/blisspixel/fitr/internal/source"
)

const DownloadPlanSchema = "fitr.discovery.download-plan.v1"

// DownloadFile specifies one explicit remote file to download with its
// component role and provider-declared checksum bounds.
type DownloadFile struct {
	Path           string `json:"path"`
	ComponentRole  string `json:"component_role"`
	SizeBytes      int64  `json:"size_bytes"`
	DeclaredSHA256 string `json:"declared_sha256"`
	Required       bool   `json:"required"`
}

// DownloadPlan establishes an owned, bounded download schedule before local
// file allocation. It does not initiate network downloads or modify storage.
type DownloadPlan struct {
	Schema             string         `json:"schema"`
	Status             string         `json:"status"` // "ready", "blocked", "unresolved", "incomplete"
	Reason             string         `json:"reason"`
	Files              []DownloadFile `json:"files,omitempty"`
	TotalRequiredBytes int64          `json:"total_required_bytes"`
	TotalOptionalBytes int64          `json:"total_optional_bytes"`
	Verification       string         `json:"verification,omitempty"` // "sha256"
}

// BuildDownloadPlan constructs a bounded download plan from a source receipt,
// checking component roles and provider-declared content SHA-256 hashes.
func BuildDownloadPlan(receipt *source.Resolution) *DownloadPlan {
	if receipt == nil {
		return nil
	}
	fileMap := make(map[string]source.FileMetadata, len(receipt.Files))
	for _, f := range receipt.Files {
		fileMap[f.Path] = f
	}

	if receipt.ComponentPlan != nil {
		return buildFromComponentPlan(receipt.ComponentPlan, fileMap)
	}

	if receipt.State != "resolved" {
		return &DownloadPlan{
			Schema: DownloadPlanSchema,
			Status: "unresolved",
			Reason: "source metadata is incomplete or unresolved",
		}
	}

	return buildFromResolvedFiles(receipt.Files)
}

func buildFromComponentPlan(cp *source.ComponentPlan, fileMap map[string]source.FileMetadata) *DownloadPlan {
	if cp.Status == source.PlanBlocked {
		return &DownloadPlan{
			Schema: DownloadPlanSchema,
			Status: "blocked",
			Reason: cp.Reason,
		}
	}
	if cp.Status == source.PlanUnresolved {
		return &DownloadPlan{
			Schema: DownloadPlanSchema,
			Status: "unresolved",
			Reason: cp.Reason,
		}
	}

	downloadFiles, totalRequired, totalOptional, allRequiredHaveHash := mapPlannedComponents(cp.Components, fileMap)
	if !allRequiredHaveHash {
		return &DownloadPlan{
			Schema:             DownloadPlanSchema,
			Status:             "incomplete",
			Reason:             "required component missing provider-declared content SHA-256",
			Files:              downloadFiles,
			TotalRequiredBytes: totalRequired,
			TotalOptionalBytes: totalOptional,
		}
	}

	return &DownloadPlan{
		Schema:             DownloadPlanSchema,
		Status:             "ready",
		Reason:             "all required components have declared sizes and content SHA-256 hashes",
		Files:              downloadFiles,
		TotalRequiredBytes: totalRequired,
		TotalOptionalBytes: totalOptional,
		Verification:       "sha256",
	}
}

func mapPlannedComponents(components []source.PlannedComponent, fileMap map[string]source.FileMetadata) ([]DownloadFile, int64, int64, bool) {
	var downloadFiles []DownloadFile
	var totalRequired, totalOptional int64
	allRequiredHaveHash := true

	for _, comp := range components {
		if comp.State == source.ComponentDisabled {
			continue
		}
		f, exists := fileMap[comp.Path]
		var size int64
		if comp.SizeBytes != nil {
			size = *comp.SizeBytes
		}
		declaredSHA := ""
		if exists {
			declaredSHA = f.DeclaredSHA256
		}

		isRequired := (comp.State == source.ComponentRequired)
		if isRequired {
			totalRequired += size
			if declaredSHA == "" {
				allRequiredHaveHash = false
			}
		} else {
			totalOptional += size
		}

		downloadFiles = append(downloadFiles, DownloadFile{
			Path:           comp.Path,
			ComponentRole:  mapComponentKindToRole(comp.Kind),
			SizeBytes:      size,
			DeclaredSHA256: declaredSHA,
			Required:       isRequired,
		})
	}
	return downloadFiles, totalRequired, totalOptional, allRequiredHaveHash
}

func buildFromResolvedFiles(files []source.FileMetadata) *DownloadPlan {
	var downloadFiles []DownloadFile
	var totalRequired int64

	for _, file := range files {
		if file.SizeBytes == nil || file.DeclaredSHA256 == "" {
			return &DownloadPlan{
				Schema: DownloadPlanSchema,
				Status: "incomplete",
				Reason: "selected file missing declared size or content SHA-256",
			}
		}
		role := "weights"
		if strings.Contains(file.Path, "-of-") {
			role = "shard"
		}
		downloadFiles = append(downloadFiles, DownloadFile{
			Path:           file.Path,
			ComponentRole:  role,
			SizeBytes:      *file.SizeBytes,
			DeclaredSHA256: file.DeclaredSHA256,
			Required:       true,
		})
		totalRequired += *file.SizeBytes
	}

	return &DownloadPlan{
		Schema:             DownloadPlanSchema,
		Status:             "ready",
		Reason:             "all selected files have declared sizes and content SHA-256 hashes",
		Files:              downloadFiles,
		TotalRequiredBytes: totalRequired,
		Verification:       "sha256",
	}
}

func mapComponentKindToRole(kind source.ComponentKind) string {
	switch kind {
	case source.ComponentKindModelShard:
		return "shard"
	case source.ComponentKindProjector:
		return "projector"
	case source.ComponentKindEncoder:
		return "encoder"
	case source.ComponentKindTokenizer:
		return "tokenizer"
	default:
		return "weights"
	}
}

// ArtifactSpec creates an artifact.Spec mapping downloaded files to target
// local paths under a target directory with their declared component roles.
func (dp *DownloadPlan) ArtifactSpec(resolutionSHA256, localDir string) (artifact.Spec, error) {
	if dp == nil || dp.Status != "ready" {
		return artifact.Spec{}, errors.New("cannot create artifact mapping from an unready download plan")
	}
	if len(dp.Files) == 0 {
		return artifact.Spec{}, errors.New("download plan has no files to map")
	}
	mappings := make([]artifact.Mapping, 0, len(dp.Files))
	for _, f := range dp.Files {
		mappings = append(mappings, artifact.Mapping{
			SourcePath:    f.Path,
			LocalPath:     filepath.Join(localDir, filepath.Base(f.Path)),
			ComponentRole: f.ComponentRole,
		})
	}
	spec := artifact.Spec{
		Schema:           artifact.SpecSchema,
		ResolutionSHA256: resolutionSHA256,
		Files:            mappings,
	}
	return spec, nil
}
