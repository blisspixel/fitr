// Package omarchy installs the embedded Quattro bar widget. The widget is a
// reader. Scoring and benchmark execution stay in fitr.
package omarchy

import (
	"bytes"
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/blisspixel/fitr/internal/atomicfile"
	"github.com/blisspixel/fitr/internal/boundedio"
	"github.com/blisspixel/fitr/internal/strictjson"
)

//go:embed manifest.json BarWidget.qml Panel.qml README.md
var payload embed.FS

const PluginID = "dev.fitr.evidence"
const markerName = ".fitr-install.json"
const markerSchema = "fitr.desktop.install.v1"

var (
	ErrUnsupported = errors.New("desktop install is unsupported on this platform")
	ErrSymlink     = errors.New("the plugin path is a symbolic link")
	ErrForeign     = errors.New("a plugin directory already exists without a fitr install marker")
	ErrMarker      = errors.New("the install marker does not match this plugin")
	ErrAbsent      = errors.New("no fitr-installed plugin is present")
	ErrParent      = errors.New("the plugins directory is not usable")
)

type installMarker struct {
	Schema      string `json:"schema"`
	ID          string `json:"id"`
	InstalledBy string `json:"installed_by"`
	FitrVersion string `json:"fitr_version"`
}

// DefaultPluginsDir is the Quattro third-party plugin directory. It exists
// only on Linux. Callers on other systems pass an explicit directory and do
// not write into a user profile by default.
func DefaultPluginsDir() (string, error) {
	if runtime.GOOS != "linux" {
		return "", ErrUnsupported
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", ErrUnsupported
	}
	return filepath.Join(home, ".config", "omarchy", "plugins"), nil
}

func PluginDir(parent string) string {
	return filepath.Join(parent, PluginID)
}

// Install copies the embedded plugin into parent/dev.fitr.evidence and writes
// the marker last. A directory without that marker is left untouched. A
// matching marker may be replaced, which also replaces files the operator
// edited inside that directory.
func Install(parent, version string) error {
	if strings.TrimSpace(version) == "" || strings.ContainsAny(version, `/\`) {
		return ErrParent
	}
	if err := usableParent(parent); err != nil {
		return err
	}
	dest := PluginDir(parent)
	info, err := os.Lstat(dest)
	created := false
	switch {
	case err == nil && info.Mode()&os.ModeSymlink != 0:
		return ErrSymlink
	case err == nil && !info.IsDir():
		return ErrForeign
	case err == nil:
		if err := matchingMarker(dest); err != nil {
			return err
		}
	case errors.Is(err, os.ErrNotExist):
		if err := os.Mkdir(dest, 0o700); err != nil {
			return ErrParent
		}
		created = true
	default:
		return ErrParent
	}
	if err := writePayload(dest, version); err != nil {
		if created {
			_ = os.RemoveAll(dest)
		}
		return err
	}
	return nil
}

// Remove deletes only a directory fitr marked as its own. It does not follow
// a symlink and it does not remove parent. Official `omarchy plugin remove`
// on a non-git folder moves it aside; this function is the explicit delete,
// and only after --yes at the CLI.
func Remove(parent string) error {
	if err := usableParent(parent); err != nil {
		return err
	}
	dest := PluginDir(parent)
	info, err := os.Lstat(dest)
	if err != nil || !info.IsDir() {
		return ErrAbsent
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return ErrSymlink
	}
	if err := matchingMarker(dest); err != nil {
		if errors.Is(err, ErrForeign) {
			return ErrMarker
		}
		return err
	}
	if err := os.RemoveAll(dest); err != nil {
		return ErrParent
	}
	return nil
}

func usableParent(parent string) error {
	if parent == "" || !filepath.IsAbs(parent) {
		return ErrParent
	}
	info, err := os.Lstat(parent)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return ErrParent
	}
	return nil
}

func writePayload(dest, version string) error {
	entries, err := fs.ReadDir(payload, ".")
	if err != nil {
		return ErrParent
	}
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		data, err := payload.ReadFile(entry.Name())
		if err != nil {
			return ErrParent
		}
		if err := atomicfile.Write(filepath.Join(dest, entry.Name()), data, 0o644); err != nil {
			return ErrParent
		}
	}
	body, err := json.Marshal(installMarker{
		Schema: markerSchema, ID: PluginID, InstalledBy: "fitr", FitrVersion: version,
	})
	if err != nil {
		return ErrParent
	}
	return atomicfile.Write(filepath.Join(dest, markerName), append(body, '\n'), 0o600)
}

func matchingMarker(dir string) error {
	data, err := boundedio.ReadFile(filepath.Join(dir, markerName), 4096)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return ErrForeign
		}
		return ErrMarker
	}
	if err := strictjson.Validate(data); err != nil {
		return ErrMarker
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var marker installMarker
	if err := decoder.Decode(&marker); err != nil {
		return ErrMarker
	}
	if marker.Schema != markerSchema || marker.ID != PluginID || marker.InstalledBy != "fitr" || strings.TrimSpace(marker.FitrVersion) == "" {
		return ErrMarker
	}
	return nil
}
