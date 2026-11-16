package main

import (
	"flag"
	"fmt"
	"strings"
)

// flagNotice turns the flag package's sentence into the error, note, and hint
// voice. The raw sentence names the flag with one dash, which is not the
// spelling help shows.
func flagNotice(fs *flag.FlagSet, err error) (string, string, string) {
	text := err.Error()
	hint := "fitr " + fs.Name() + " --help"
	if value, name, ok := invalidFlagValue(text); ok {
		return fmt.Sprintf("invalid --%s value %q", name, value), flagUsageNote(fs, name), hint
	}
	if name, ok := strings.CutPrefix(text, "flag needs an argument: "); ok {
		name = strings.TrimLeft(name, "-")
		return "missing --" + name + " value", flagUsageNote(fs, name), hint
	}
	if raw, ok := strings.CutPrefix(text, "flag provided but not defined: "); ok {
		raw = strings.TrimSpace(raw)
		return "unknown flag --" + strings.TrimLeft(raw, "-"), "", hint
	}
	return "invalid arguments", text, hint
}

func invalidFlagValue(text string) (string, string, bool) {
	const prefix = "invalid value "
	rest, ok := strings.CutPrefix(text, prefix)
	if !ok || len(rest) < 2 || rest[0] != '"' {
		return "", "", false
	}
	end := strings.Index(rest[1:], `"`)
	if end < 0 {
		return "", "", false
	}
	value := rest[1 : 1+end]
	rest = rest[1+end+1:]
	const marker = " for flag "
	rest, ok = strings.CutPrefix(rest, marker)
	if !ok {
		return "", "", false
	}
	name, _, _ := strings.Cut(rest, ":")
	name = strings.TrimLeft(strings.TrimSpace(name), "-")
	if name == "" {
		return "", "", false
	}
	return value, name, true
}

func flagUsageNote(fs *flag.FlagSet, name string) string {
	if fs == nil {
		return "the value is not the form this flag accepts"
	}
	parsed := fs.Lookup(name)
	if parsed == nil || parsed.Usage == "" {
		return "the value is not the form this flag accepts"
	}
	return parsed.Usage
}

// userCommands is the hint list. help and version are dispatched beside
// commandHandler. A new command belongs here, in that dispatcher, and on the
// main help page. screenshots is dev-only and stays off that page.
var userCommands = []string{
	"advise", "apply", "artifact", "auto", "board", "calibrate", "cleanup",
	"compare", "decide", "desktop", "device", "diag", "discover", "doctor",
	"experiment", "export", "help", "mcp", "profiles", "role", "run",
	"screenshots", "source", "suggest", "tailor", "top", "tune", "update",
	"version", "view",
}

// closestCommand reports a command within two single-character edits.
// A model reference, a path, and an exact command name are not hints.
// The hint is not an inventory check. fitr advise <name> remains the exact
// path, including a library name that happens to sit close to a command.
func closestCommand(typed string) (string, bool) {
	if typed == "" || strings.ContainsAny(typed, ":/\\.") {
		return "", false
	}
	folded := strings.ToLower(typed)
	best := ""
	bestDistance := 3
	for _, name := range userCommands {
		if folded == name {
			if typed == name {
				return "", false
			}
			return name, true
		}
		distance := editDistance(folded, name)
		if distance > 2 {
			continue
		}
		if distance < bestDistance || (distance == bestDistance && name < best) {
			best = name
			bestDistance = distance
		}
	}
	if best == "" {
		return "", false
	}
	return best, true
}

func editDistance(left, right string) int {
	if left == right {
		return 0
	}
	if len(left) > len(right) {
		left, right = right, left
	}
	if len(right)-len(left) > 2 {
		return 3
	}
	previous := make([]int, len(left)+1)
	for i := range previous {
		previous[i] = i
	}
	for j := 1; j <= len(right); j++ {
		current := make([]int, len(left)+1)
		current[0] = j
		rowMin := current[0]
		for i := 1; i <= len(left); i++ {
			cost := 1
			if left[i-1] == right[j-1] {
				cost = 0
			}
			current[i] = min(previous[i]+1, current[i-1]+1, previous[i-1]+cost)
			if current[i] < rowMin {
				rowMin = current[i]
			}
		}
		if rowMin > 2 {
			return 3
		}
		previous = current
	}
	return previous[len(left)]
}
