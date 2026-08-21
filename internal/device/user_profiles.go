package device

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// UserProfilesDir is ~/.fitr/profiles, or $FITR_PROFILES. User files override
// embedded profiles of the same name and are tried first for GPU/host match.
func UserProfilesDir() string {
	if d := os.Getenv("FITR_PROFILES"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join("profiles")
	}
	return filepath.Join(home, ".fitr", "profiles")
}

func loadUserProfiles() ([]Profile, error) {
	dir := UserProfilesDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []Profile
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		path := filepath.Join(dir, e.Name())
		b, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("profile %s: %w", path, err)
		}
		p, err := decodeProfile(path, b)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func decodeProfile(source string, b []byte) (Profile, error) {
	var p Profile
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&p); err != nil {
		return Profile{}, fmt.Errorf("profile %s: %w", source, err)
	}
	var trailing any
	if err := dec.Decode(&trailing); err != io.EOF {
		if err == nil {
			return Profile{}, fmt.Errorf("profile %s: content after the JSON value", source)
		}
		return Profile{}, fmt.Errorf("profile %s: %w", source, err)
	}
	if p.Name == "" {
		return Profile{}, fmt.Errorf("profile %s: missing name", source)
	}
	for gateName, gate := range p.Gates {
		if _, ok := gate["why"]; !ok {
			return Profile{}, fmt.Errorf("profile %s: gate %q has no why", source, gateName)
		}
	}
	return p, nil
}

// ScaffoldProfile copies default's gates onto a new UNCALIBRATED profile
// matched to this machine. The numbers are starting points, not measurements.
func ScaffoldProfile(name string, fp Fingerprint) (Profile, error) {
	profs, err := LoadEmbeddedProfiles()
	if err != nil {
		return Profile{}, err
	}
	var def Profile
	for _, p := range profs {
		if p.Name == "default" {
			def = p
			break
		}
	}
	if def.Name == "" {
		return Profile{}, fmt.Errorf("embedded default profile missing")
	}
	name = slugProfile(name)
	if name == "" {
		name = slugProfile(fp.Host)
	}
	if name == "" {
		name = "local"
	}
	p := def
	p.Name = name
	p.Description = "UNCALIBRATED starting point for this machine; edit gates after you run models you already have opinions about"
	p.Match = map[string]string{}
	if fp.GPU != "" {
		p.Match["gpu_contains"] = fp.GPU
	} else if fp.Host != "" {
		p.Match["host"] = fp.Host
	}
	p.Notes = append([]string{
		"UNCALIBRATED. Scaffolded " + time.Now().UTC().Format("2006-01-02") + " from default.",
		"These numbers are not measurements of this GPU. Run models you already have opinions about, then edit the gates so the verdicts match lived experience.",
		"Do not publish this file as a calibrated community profile.",
	}, p.Notes...)
	return p, nil
}

func WriteProfile(dir string, p Profile) (string, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	path := filepath.Join(dir, slugProfile(p.Name)+".json")
	if _, err := os.Stat(path); err == nil {
		return "", fmt.Errorf("already exists: %s", path)
	}
	b, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return "", err
	}
	return path, os.WriteFile(path, append(b, '\n'), 0o644)
}

func slugProfile(s string) string {
	s = strings.TrimSpace(strings.ToLower(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			lastDash = false
		case r == '-' || r == '_' || r == ' ' || r == '.':
			if !lastDash && b.Len() > 0 {
				b.WriteByte('-')
				lastDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
