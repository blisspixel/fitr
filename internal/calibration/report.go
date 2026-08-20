// Package calibration builds privacy-safe evidence from paired check runs.
package calibration

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/blisspixel/fitr/internal/eval"
)

const (
	PairSchema    = "fitr.calibration.pair.v1"
	SummarySchema = "fitr.calibration.summary.v1"
)

// Device contains the measurement-relevant hardware fields but deliberately
// omits the hostname. DeviceID lets aggregation deduplicate a box without
// publishing that hostname.
type Device struct {
	ID              string            `json:"id"`
	OS              string            `json:"os"`
	CPU             string            `json:"cpu"`
	RAMGB           float64           `json:"ram_gb"`
	GPU             string            `json:"gpu"`
	GPUDriver       string            `json:"gpu_driver"`
	GPUDriverDate   string            `json:"gpu_driver_date,omitempty"`
	GPUBackend      string            `json:"gpu_backend,omitempty"`
	Runtime         string            `json:"runtime"`
	InferenceDevice string            `json:"inference_device"`
	Config          map[string]string `json:"config,omitempty"`
}

// PseudonymousDeviceID hashes the full local comparison key. The report can
// recognize repeat submissions from one box without carrying the hostname.
// The stable value can link reports from that box, so it is pseudonymous rather
// than anonymous.
func PseudonymousDeviceID(deviceKey string) string {
	sum := sha256.Sum256([]byte("fitr-calibration-v1\x00" + deviceKey))
	return hex.EncodeToString(sum[:8])
}

// Run identifies one side of a calibration pair. It contains no prompts or
// model output.
type Run struct {
	Model               string `json:"model"`
	Quant               string `json:"quant,omitempty"`
	Family              string `json:"family,omitempty"`
	ParameterSize       string `json:"parameter_size,omitempty"`
	StartedAt           string `json:"started_at"`
	NumCtx              int    `json:"num_ctx"`
	ResultSchemaVersion int    `json:"result_schema_version"`
}

// Item is the paired pass/fail agreement for one generated task family.
type Item struct {
	TaskID          string `json:"task"`
	Family          string `json:"family"`
	Need            string `json:"need"`
	Shared          int    `json:"shared"`
	Flips           int    `json:"flips"`
	ReferencePasses int    `json:"reference_passes"`
	CandidatePasses int    `json:"candidate_passes"`
}

// PairReport is the shareable evidence from two paired runs. Raw prompts,
// model responses, result paths, and hostnames are intentionally absent.
type PairReport struct {
	Schema      string `json:"schema"`
	CreatedAt   string `json:"created_at"`
	FitrVersion string `json:"fitr_version"`
	SpecVersion int    `json:"spec_version"`
	SeedSet     string `json:"seedset"`
	Device      Device `json:"device"`

	Reference Run `json:"reference"`
	Candidate Run `json:"candidate"`

	Shared        int    `json:"shared"`
	Flips         int    `json:"flips"`
	Discriminated int    `json:"items_discriminated"`
	NeverObserved int    `json:"items_never_observed"`
	Direction     string `json:"direction"`
	Items         []Item `json:"items"`
}

// NewPair builds a report and places the higher ranked quant in Reference
// when the dtypes have a known ordering. Unknown schemes retain input order.
func NewPair(fitrVersion string, specVersion int, seedSet string, device Device,
	a, b Run, stats []eval.ItemStat) PairReport {
	stats = append([]eval.ItemStat(nil), stats...)
	device.OS = strings.TrimSpace(device.OS)
	device.CPU = strings.Join(strings.Fields(device.CPU), " ")
	device.GPU = strings.Join(strings.Fields(device.GPU), " ")
	device.RAMGB = math.Round(device.RAMGB*10) / 10
	device.Config = shareableConfig(device.Config)
	direction := "input_order"
	ra, rb := eval.QuantRank(a.Quant), eval.QuantRank(b.Quant)
	if ra > 0 && rb > 0 && ra != rb {
		direction = "higher_precision_reference"
		if rb > ra {
			a, b = b, a
			for i := range stats {
				stats[i].APass, stats[i].BPass = stats[i].BPass, stats[i].APass
			}
		}
	}

	r := PairReport{
		Schema: PairSchema, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		FitrVersion: fitrVersion, SpecVersion: specVersion, SeedSet: seedSet,
		Device: device, Reference: a, Candidate: b, Direction: direction,
	}
	for _, s := range stats {
		r.Shared += s.Shared
		r.Flips += s.Flips
		if s.Discriminated() {
			r.Discriminated++
		} else {
			r.NeverObserved++
		}
		r.Items = append(r.Items, Item{
			TaskID: s.TaskID, Family: s.Family, Need: s.Need,
			Shared: s.Shared, Flips: s.Flips,
			ReferencePasses: s.APass, CandidatePasses: s.BPass,
		})
	}
	sort.Slice(r.Items, func(i, j int) bool {
		if r.Items[i].Flips != r.Items[j].Flips {
			return r.Items[i].Flips > r.Items[j].Flips
		}
		return r.Items[i].TaskID < r.Items[j].TaskID
	})
	return r
}

func shareableConfig(config map[string]string) map[string]string {
	allowed := map[string]bool{
		"OLLAMA_IGPU_ENABLE":       true,
		"OLLAMA_FLASH_ATTENTION":   true,
		"OLLAMA_KV_CACHE_TYPE":     true,
		"OLLAMA_MAX_LOADED_MODELS": true,
		"OLLAMA_NUM_PARALLEL":      true,
		"OLLAMA_CONTEXT_LENGTH":    true,
		"LLAMA_ARG_FIT":            true,
	}
	out := map[string]string{}
	for key, value := range config {
		if allowed[key] && value != "" {
			out[key] = value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// WriteJSON writes an indented report or summary.
func WriteJSON(path string, value any) error {
	if strings.TrimSpace(path) == "" {
		return errors.New("missing output path")
	}
	b, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		return err
	}
	b = append(b, '\n')
	return os.WriteFile(path, b, 0o644)
}

// ReadPair loads one pair report and rejects unrelated JSON.
func ReadPair(path string) (PairReport, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return PairReport{}, err
	}
	var r PairReport
	if err := json.Unmarshal(b, &r); err != nil {
		return PairReport{}, err
	}
	if err := validatePair(r); err != nil {
		return PairReport{}, fmt.Errorf("%s: %w", path, err)
	}
	return r, nil
}

func validatePair(r PairReport) error {
	if r.Schema != PairSchema {
		return fmt.Errorf("schema %q is not %q", r.Schema, PairSchema)
	}
	if r.Device.ID == "" || r.SeedSet == "" || r.SpecVersion < 1 || len(r.Items) == 0 {
		return errors.New("incomplete calibration report")
	}
	if len(r.Device.ID) != 16 {
		return errors.New("device id is not a fitr pseudonymous identifier")
	}
	if _, err := hex.DecodeString(r.Device.ID); err != nil {
		return errors.New("device id is not a fitr pseudonymous identifier")
	}
	if r.Reference.Model == "" || r.Candidate.Model == "" ||
		r.Reference.Family == "" || !strings.EqualFold(r.Reference.Family, r.Candidate.Family) ||
		r.Reference.ParameterSize == "" || !strings.EqualFold(r.Reference.ParameterSize, r.Candidate.ParameterSize) {
		return errors.New("report is not a same-family, same-size model pair")
	}
	if r.Reference.ResultSchemaVersion < 1 || r.Reference.ResultSchemaVersion != r.Candidate.ResultSchemaVersion {
		return errors.New("result schema is missing or differs across the pair")
	}
	seen := map[string]bool{}
	shared, flips, discriminated := 0, 0, 0
	for _, item := range r.Items {
		if item.TaskID == "" || seen[item.TaskID] {
			return fmt.Errorf("missing or duplicate task id %q", item.TaskID)
		}
		if item.Shared < 1 || item.Flips < 0 || item.Flips > item.Shared ||
			item.ReferencePasses < 0 || item.ReferencePasses > item.Shared ||
			item.CandidatePasses < 0 || item.CandidatePasses > item.Shared {
			return fmt.Errorf("invalid counts for task %q", item.TaskID)
		}
		seen[item.TaskID] = true
		shared += item.Shared
		flips += item.Flips
		if item.Flips > 0 {
			discriminated++
		}
	}
	if r.Shared != shared || r.Flips != flips || r.Discriminated != discriminated ||
		r.NeverObserved != len(r.Items)-discriminated {
		return errors.New("pair totals do not match item outcomes")
	}
	return nil
}

// SummaryItem combines the evidence for one task without deciding whether to
// keep or drop it. That decision needs coverage from multiple boxes and pairs.
type SummaryItem struct {
	TaskID               string `json:"task"`
	Family               string `json:"family"`
	Need                 string `json:"need"`
	Reports              int    `json:"reports"`
	Devices              int    `json:"devices"`
	Shared               int    `json:"shared"`
	Flips                int    `json:"flips"`
	DiscriminatedReports int    `json:"discriminated_reports"`
	DiscriminatedDevices int    `json:"discriminated_devices"`
	Status               string `json:"status"`
}

// Summary aggregates independent pair reports. It labels observed evidence
// but never automates deletion from the task battery.
type Summary struct {
	Schema      string        `json:"schema"`
	CreatedAt   string        `json:"created_at"`
	SpecVersion int           `json:"spec_version"`
	Reports     int           `json:"reports"`
	Devices     int           `json:"devices"`
	ModelPairs  int           `json:"model_pairs"`
	Items       []SummaryItem `json:"items"`
}

// Aggregate combines reports that used the same task specification. Exact
// duplicate submissions are rejected so one run cannot silently gain weight.
func Aggregate(reports []PairReport) (Summary, error) {
	if len(reports) == 0 {
		return Summary{}, errors.New("no calibration reports")
	}
	specVersion := reports[0].SpecVersion
	type itemAcc struct {
		family, need                                 string
		reports, shared, flips, discriminatedReports int
		devices, discriminatedDevices                map[string]bool
	}
	byItem := map[string]*itemAcc{}
	devices := map[string]bool{}
	pairs := map[string]bool{}
	seen := map[string]bool{}
	var expectedItems map[string]bool
	for index, r := range reports {
		if err := validatePair(r); err != nil {
			return Summary{}, fmt.Errorf("report %d: %w", index+1, err)
		}
		if r.SpecVersion != specVersion {
			return Summary{}, fmt.Errorf("spec version mismatch: %d and %d", specVersion, r.SpecVersion)
		}
		key := strings.Join([]string{r.Device.ID, r.SeedSet, r.Reference.Model, r.Candidate.Model}, "\x00")
		if seen[key] {
			return Summary{}, fmt.Errorf("duplicate report for device %s, seedset %s, pair %s / %s",
				r.Device.ID, r.SeedSet, r.Reference.Model, r.Candidate.Model)
		}
		seen[key] = true
		devices[r.Device.ID] = true
		pairs[r.Reference.Model+"\x00"+r.Candidate.Model] = true
		itemSet := map[string]bool{}
		for _, item := range r.Items {
			if item.TaskID == "" || itemSet[item.TaskID] {
				return Summary{}, fmt.Errorf("report has missing or duplicate task id %q", item.TaskID)
			}
			itemSet[item.TaskID] = true
		}
		if expectedItems == nil {
			expectedItems = itemSet
		} else if !sameSet(expectedItems, itemSet) {
			return Summary{}, errors.New("calibration reports contain different task sets")
		}
		for _, item := range r.Items {
			a := byItem[item.TaskID]
			if a == nil {
				a = &itemAcc{family: item.Family, need: item.Need,
					devices: map[string]bool{}, discriminatedDevices: map[string]bool{}}
				byItem[item.TaskID] = a
			} else if a.family != item.Family || a.need != item.Need {
				return Summary{}, fmt.Errorf("task %q changed family or need across reports", item.TaskID)
			}
			a.reports++
			a.shared += item.Shared
			a.flips += item.Flips
			a.devices[r.Device.ID] = true
			if item.Flips > 0 {
				a.discriminatedReports++
				a.discriminatedDevices[r.Device.ID] = true
			}
		}
	}

	s := Summary{
		Schema: SummarySchema, CreatedAt: time.Now().UTC().Format(time.RFC3339),
		SpecVersion: specVersion, Reports: len(reports), Devices: len(devices), ModelPairs: len(pairs),
	}
	for id, a := range byItem {
		status := "not_observed"
		if a.flips > 0 {
			status = "observed"
		}
		s.Items = append(s.Items, SummaryItem{
			TaskID: id, Family: a.family, Need: a.need,
			Reports: a.reports, Devices: len(a.devices), Shared: a.shared, Flips: a.flips,
			DiscriminatedReports: a.discriminatedReports,
			DiscriminatedDevices: len(a.discriminatedDevices), Status: status,
		})
	}
	sort.Slice(s.Items, func(i, j int) bool {
		if s.Items[i].DiscriminatedDevices != s.Items[j].DiscriminatedDevices {
			return s.Items[i].DiscriminatedDevices > s.Items[j].DiscriminatedDevices
		}
		if s.Items[i].Flips != s.Items[j].Flips {
			return s.Items[i].Flips > s.Items[j].Flips
		}
		return s.Items[i].TaskID < s.Items[j].TaskID
	})
	return s, nil
}

func sameSet(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for key := range a {
		if !b[key] {
			return false
		}
	}
	return true
}
