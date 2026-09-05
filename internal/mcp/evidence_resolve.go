package mcp

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const maxEvidenceAliasLinks = 40

func evidencePathParts(path string) []string {
	return strings.FieldsFunc(path, func(char rune) bool { return char == '/' || (filepath.Separator == '\\' && char == '\\') })
}

// Resolve local aliases component by component. Every link target is checked
// before target metadata is read, so a static local link cannot redirect this
// reader to a UNC share or device namespace. The final standard normalization
// preserves Windows short-name/case behavior after all links were inspected.
// This is not protection against an authorized writer racing these path reads.
func resolveLocalEvidence(path string) (string, error) {
	if err := localEvidencePath(path); err != nil {
		return "", err
	}
	if !filepath.IsAbs(path) {
		return "", errors.New("evidence resolution requires a native absolute path")
	}
	volume := filepath.VolumeName(path)
	current := volume + string(filepath.Separator)
	pending := evidencePathParts(path[len(volume):])
	links := 0
	for steps := 0; len(pending) > 0; steps++ {
		if steps >= 512 || len(current)+len(strings.Join(pending, string(filepath.Separator))) > 4096 {
			return "", errors.New("evidence alias expansion exceeds its limit")
		}
		part := pending[0]
		pending = pending[1:]
		if part == "." {
			continue
		}
		if part == ".." {
			current = filepath.Dir(current)
			continue
		}
		candidate := filepath.Join(current, part)
		info, err := os.Lstat(candidate)
		if err != nil {
			return "", err
		}
		if info.Mode()&os.ModeSymlink == 0 {
			if len(pending) > 0 && !info.IsDir() {
				return "", errors.New("evidence alias ancestor is not a directory")
			}
			current = candidate
			continue
		}
		links++
		if links > maxEvidenceAliasLinks {
			return "", errors.New("evidence alias expansion exceeds its link limit")
		}
		target, err := os.Readlink(candidate)
		if err != nil {
			return "", err
		}
		current, pending, err = expandEvidenceAlias(current, target, pending)
		if err != nil {
			return "", err
		}
	}
	return filepath.EvalSymlinks(current)
}

func expandEvidenceAlias(current, target string, remaining []string) (string, []string, error) {
	if err := localEvidenceSpelling(target, true); err != nil {
		return "", nil, err
	}
	volume := filepath.VolumeName(target)
	if filepath.IsAbs(target) {
		current = volume + string(filepath.Separator)
		target = target[len(volume):]
	} else if volume != "" || os.IsPathSeparator(target[0]) || strings.Contains(target, ":") {
		return "", nil, errors.New("evidence alias target must be local relative or native absolute")
	}
	return current, append(evidencePathParts(target), remaining...), nil
}
