package artifact

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
)

const hashChunkBytes = 1 << 20

func hashFiles(ctx context.Context, result *Binding, opened []openedFile) {
	buffer := make([]byte, hashChunkBytes)
	for index := range result.Files {
		observation := &result.Files[index]
		if observation.State != "not_read" {
			continue
		}
		digest, count, state := readHash(ctx, opened[index].file, observation.Before.SizeBytes, result.Limits.MaxBytes-result.BytesRead, buffer)
		observation.BytesRead, observation.ObservedSHA256, observation.State = count, digest, state
		result.BytesRead += count
		if state == "changed" {
			observation.IdentityState = "changed"
		}
		if state == "hashed" {
			metadata := sourceFile(result.Source, observation.SourcePath)
			observation.State = "locally_hashed"
			if metadata.DeclaredSHA256 != "" {
				observation.State = "matched"
				if metadata.DeclaredSHA256 != digest {
					observation.State = "hash_mismatch"
				}
			}
		}
	}
}

// Read exactly the preflight size. No extra EOF probe is needed or charged:
// retained-handle and path metadata are rechecked before the receipt is sealed.
func readHash(ctx context.Context, reader io.Reader, size, budget int64, buffer []byte) (string, int64, string) {
	hash := sha256.New()
	var count int64
	for count < size {
		if state := stopped(ctx); state != "" {
			return "", count, state
		}
		amount := min(int64(len(buffer)), size-count, budget-count)
		if amount <= 0 {
			return "", count, "budget_exceeded"
		}
		read, err := reader.Read(buffer[:amount])
		count += int64(read)
		if read > 0 {
			_, _ = hash.Write(buffer[:read])
		}
		if state := stopped(ctx); state != "" {
			return "", count, state
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return "", count, "read_error"
		}
		if errors.Is(err, io.EOF) && count != size {
			return "", count, "changed"
		}
		if read == 0 {
			return "", count, "read_error"
		}
	}
	if state := stopped(ctx); state != "" {
		return "", count, state
	}
	return "sha256:" + hex.EncodeToString(hash.Sum(nil)), count, "hashed"
}

func finalRecheck(ctx context.Context, result *Binding, opened []openedFile) {
	for index := range result.Files {
		observation := &result.Files[index]
		if observation.State == "cancelled" || observation.State == "timeout" || observation.State == "read_error" || observation.State == "changed" {
			continue
		}
		if state := stopped(ctx); state != "" {
			if hashedState(observation.State) {
				observation.State, observation.ObservedSHA256 = state, ""
			}
			continue
		}
		pathInfo, err := os.Lstat(observation.LocalPath)
		if observation.State == "missing" {
			if err == nil {
				markChanged(observation, pathInfo)
			}
			continue
		}
		if err != nil || rejectLinks(observation.LocalPath, false) != nil || !sameFacts(opened[index].before, pathInfo) {
			markChanged(observation, nil)
			continue
		}
		if hashedState(observation.State) {
			recheckHandle(observation, opened[index], pathInfo)
		}
	}
}

func recheckHandle(observation *FileObservation, opened openedFile, pathInfo os.FileInfo) {
	info, err := opened.file.Stat()
	if err != nil || !sameFacts(opened.before, info) || !sameFacts(info, pathInfo) {
		markChanged(observation, info)
		return
	}
	observation.After, observation.IdentityState = facts(info), "verified"
}

func markChanged(observation *FileObservation, info os.FileInfo) {
	observation.State, observation.ObservedSHA256, observation.IdentityState = "changed", "", "changed"
	if info != nil && info.Size() >= 0 {
		observation.After = facts(info)
	}
}

func hashedState(state string) bool {
	return state == "matched" || state == "locally_hashed" || state == "hash_mismatch"
}
