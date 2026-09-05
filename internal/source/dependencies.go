package source

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

var shardPattern = regexp.MustCompile(`^(.+)-([0-9]{5})-of-([0-9]{5})(\.[A-Za-z0-9._-]+)$`)

func findDependencies(selected []string, inventory map[string]FileMetadata) ([]DependencyFinding, error) {
	findings := []DependencyFinding{}
	for _, kind := range []string{"cross_repository", "encoder", "projector", "tokenizer"} {
		findings = append(findings, DependencyFinding{Kind: kind, Status: "unknown", Basis: "not_inspected"})
	}
	seen := make(map[string]bool)
	for _, file := range selected {
		shards, err := shardDependencies(file, selected, inventory)
		if err != nil {
			return nil, err
		}
		for _, shard := range shards {
			key := shard.Kind + "\x00" + shard.TargetFile
			if !seen[key] {
				findings = append(findings, shard)
				seen[key] = true
			}
		}
		if len(findings) > MaxDependencies {
			return nil, errors.New("dependency findings exceed limit")
		}
	}
	for path := range inventory {
		kind := candidateKind(path)
		if kind == "" {
			continue
		}
		findings = append(findings, DependencyFinding{Kind: kind, TargetFile: path,
			Status: "candidate", Basis: "filename_only"})
		if len(findings) > MaxDependencies {
			return nil, errors.New("dependency findings exceed limit")
		}
	}
	slices.SortFunc(findings, compareDependency)
	return findings, nil
}

func candidateKind(path string) string {
	lower := strings.ToLower(path)
	switch {
	case strings.Contains(lower, "mmproj") || strings.Contains(lower, "projector"):
		return "projector"
	case strings.Contains(lower, "text_encoder") || strings.Contains(lower, "vision_encoder") || strings.Contains(lower, "encoder"):
		return "encoder"
	default:
		return ""
	}
}

func shardDependencies(path string, selected []string, inventory map[string]FileMetadata) ([]DependencyFinding, error) {
	parts := shardPattern.FindStringSubmatch(path)
	if parts == nil {
		return nil, nil
	}
	index, _ := strconv.Atoi(parts[2])
	total, _ := strconv.Atoi(parts[3])
	if total < 1 || total > MaxDependencies || index < 1 || index > total {
		return nil, errors.New("invalid or excessive numbered shard group")
	}
	for candidate := range inventory {
		other := shardPattern.FindStringSubmatch(candidate)
		if len(other) == 5 && other[1] == parts[1] && other[4] == parts[4] && other[3] != parts[3] {
			return nil, errors.New("conflicting numbered shard totals")
		}
	}
	findings := []DependencyFinding{}
	for number := 1; number <= total; number++ {
		target := fmt.Sprintf("%s-%05d-of-%05d%s", parts[1], number, total, parts[4])
		if slices.Contains(selected, target) {
			continue
		}
		status := "missing"
		if _, present := inventory[target]; present {
			status = "unselected"
		}
		findings = append(findings, DependencyFinding{Kind: "shard", SourceFile: path,
			TargetFile: target, Status: status, Basis: "numbered_filename"})
	}
	return findings, nil
}

func compareDependency(left, right DependencyFinding) int {
	return strings.Compare(dependencyKey(left), dependencyKey(right))
}

func dependencyKey(finding DependencyFinding) string {
	return finding.Kind + "\x00" + finding.TargetFile + "\x00" + finding.SourceFile + "\x00" + finding.Status
}
