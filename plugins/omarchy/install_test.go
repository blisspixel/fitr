package omarchy

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEmbeddedPluginMatchesDisk(t *testing.T) {
	for _, name := range []string{"manifest.json", "BarWidget.qml", "Panel.qml", "README.md"} {
		embedded, err := payload.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		disk, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(embedded, disk) {
			t.Fatalf("%s drifted from the embed", name)
		}
	}
	var manifest struct {
		SchemaVersion int               `json:"schemaVersion"`
		ID            string            `json:"id"`
		License       string            `json:"license"`
		Kinds         []string          `json:"kinds"`
		EntryPoints   map[string]string `json:"entryPoints"`
		Omarchy       map[string]any    `json:"omarchy"`
	}
	if err := json.Unmarshal(mustPayload(t, "manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest.SchemaVersion != 1 || manifest.ID != PluginID || manifest.License != "Apache-2.0" {
		t.Fatalf("%+v", manifest)
	}
	if len(manifest.Kinds) != 1 || manifest.Kinds[0] != "bar-widget" || manifest.EntryPoints["barWidget"] != "BarWidget.qml" {
		t.Fatalf("%+v", manifest)
	}
	if manifest.Omarchy != nil {
		t.Fatal("clone metadata would bind this plugin to a built-in")
	}
}

func TestQMLStaysReadOnlyUntilAnExplicitBenchmark(t *testing.T) {
	bar := string(mustPayload(t, "BarWidget.qml"))
	panel := string(mustPayload(t, "Panel.qml"))
	for _, forbidden := range []string{"sh -c", "curl", "wget", "http://", "https://", "--pull", ".argv"} {
		if strings.Contains(bar, forbidden) || strings.Contains(panel, forbidden) {
			t.Fatalf("qml contains %q", forbidden)
		}
	}
	if !strings.Contains(bar, `[bin, "desktop", "status", "--display", "json"]`) {
		t.Fatal("status command drifted")
	}
	if !strings.Contains(bar, `[bin, "desktop", "benchmark", "--role", role, "--confirm"]`) {
		t.Fatal("benchmark command drifted")
	}
	if !strings.Contains(panel, "root.requestMeasurement = false") || !strings.Contains(panel, "refreshStatus") {
		t.Fatal("opening the panel does not stay read-only")
	}
	if strings.Contains(panel, "runBenchmark") && strings.Contains(panel, "function open()") {
		open := panel[strings.Index(panel, "function open()"):]
		open = open[:strings.Index(open, "function close()")]
		if strings.Contains(open, "runBenchmark") {
			t.Fatal("open starts a benchmark")
		}
	}
	if strings.Contains(bar, "estimated_fit") || strings.Contains(bar, "observed_fit") {
		t.Fatal("bar label reads a fit value")
	}
}

func TestPanelIsAGlance(t *testing.T) {
	panel := string(mustPayload(t, "Panel.qml"))
	bar := string(mustPayload(t, "BarWidget.qml"))
	for _, gone := range []string{
		"This status reads sealed local evidence.",
		"Opening this panel does not download",
		"Request measurement",
		"Measurement stays displayed",
		"document.limits",
	} {
		if strings.Contains(panel, gone) {
			t.Fatalf("panel still lectures with %q", gone)
		}
	}
	if strings.Contains(bar, "document.model") {
		t.Fatal("bar chip uses the model name")
	}
	if !strings.Contains(bar, `!== "empty"`) {
		t.Fatal("an empty role still occupies the bar")
	}
	if !strings.Contains(bar, "active: root.barUrgent") {
		t.Fatal("an urgent state does not take the shell urgent colour on the chip")
	}
	if !strings.Contains(panel, "visible: root.measurementReady()") {
		t.Fatal("a run control is offered when locality is not proven")
	}
	if !strings.Contains(bar, "root.bar.vertical") {
		t.Fatal("a vertical bar still paints the state word into a 28px slot")
	}
}

func TestUrgentRowsFollowTheShellAndStayBold(t *testing.T) {
	panel := string(mustPayload(t, "Panel.qml"))
	bar := string(mustPayload(t, "BarWidget.qml"))
	if strings.Contains(panel, "#") || strings.Contains(bar, "#") {
		t.Fatal("qml still has a colour literal")
	}
	if strings.Count(panel, "font.bold: root.urgent(modelData.state)") != 1 || !strings.Contains(panel, "font.bold: root.urgent(text)") || !strings.Contains(panel, "root.bar.urgent") {
		t.Fatal("urgent rows do not take the shell colour and weight")
	}
}

func TestStatusStartFailureSaysUnavailable(t *testing.T) {
	bar := string(mustPayload(t, "BarWidget.qml"))
	status := bar[strings.Index(bar, "id: statusProcess"):]
	status = status[:strings.Index(status, "id: benchmarkProcess")]
	if !strings.Contains(status, "onRunningChanged") || !strings.Contains(status, `failStatus("unavailable", "fitr desktop status did not start.")`) {
		t.Fatal("a status process that never exits still falls through to ?")
	}
	if !strings.Contains(status, "statusExitSeen") {
		t.Fatal("start failure would overwrite a finished status")
	}
}

func TestInstallAndRemoveHonorTheMarker(t *testing.T) {
	parent := t.TempDir()
	if err := Install(parent, "0.11.2"); err != nil {
		t.Fatal(err)
	}
	dest := PluginDir(parent)
	marker := filepath.Join(dest, markerName)
	if _, err := os.Stat(filepath.Join(dest, "manifest.json")); err != nil {
		t.Fatal(err)
	}
	if runtime.GOOS != "windows" {
		info, err := os.Stat(marker)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o600 {
			t.Fatalf("marker mode %o", info.Mode().Perm())
		}
	}
	if err := Install(parent, "0.11.2"); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(parent, "other.plugin")
	if err := os.Mkdir(foreign, 0o700); err != nil {
		t.Fatal(err)
	}
	kept := filepath.Join(foreign, "keep.txt")
	if err := os.WriteFile(kept, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Remove(parent); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dest); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("marked plugin directory survived remove")
	}
	if _, err := os.Stat(kept); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(parent); err != nil {
		t.Fatal("parent plugins directory was removed")
	}
}

func TestInstallRefusesForeignAndSymlinkDirectories(t *testing.T) {
	parent := t.TempDir()
	dest := PluginDir(parent)
	if err := os.Mkdir(dest, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(dest, "sentinel.txt")
	if err := os.WriteFile(sentinel, []byte("foreign"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := Install(parent, "0.11.2"); !errors.Is(err, ErrForeign) {
		t.Fatalf("foreign install: %v", err)
	}
	if err := Remove(parent); !errors.Is(err, ErrMarker) {
		t.Fatalf("foreign remove: %v", err)
	}
	if data, err := os.ReadFile(sentinel); err != nil || string(data) != "foreign" {
		t.Fatalf("foreign plugin changed: %v %q", err, data)
	}
	if err := os.RemoveAll(dest); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(parent, dest); err != nil {
		t.Skip("symlink unavailable")
	}
	if err := Install(parent, "0.11.2"); !errors.Is(err, ErrSymlink) {
		t.Fatalf("symlink install: %v", err)
	}
	if err := Remove(parent); !errors.Is(err, ErrSymlink) && !errors.Is(err, ErrAbsent) {
		t.Fatalf("symlink remove: %v", err)
	}
	if _, err := os.Lstat(dest); err != nil {
		t.Fatal("symlink was deleted")
	}
}

func TestDefaultPluginsDirStaysOffTheUserProfile(t *testing.T) {
	dir, err := DefaultPluginsDir()
	if runtime.GOOS == "linux" {
		if err != nil || !strings.HasSuffix(filepath.Clean(dir), filepath.Join(".config", "omarchy", "plugins")) {
			t.Fatalf("dir %q err %v", dir, err)
		}
		return
	}
	if !errors.Is(err, ErrUnsupported) || dir != "" {
		t.Fatalf("dir %q err %v", dir, err)
	}
}

func mustPayload(t *testing.T, name string) []byte {
	t.Helper()
	data, err := payload.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
