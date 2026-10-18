package experiment

import (
	"strings"

	"github.com/blisspixel/fitr/internal/eval"
	"github.com/blisspixel/fitr/internal/record"
	"github.com/blisspixel/fitr/internal/score"
	"github.com/blisspixel/fitr/internal/stats"
)

const QuantHealthSchema = "fitr.quant-health.v1"

// QuantHealth projects behavior the records already store. It is not a
// ranking and it does not attribute a difference to quantization. An unread
// cache dtype is not treated as the same configuration. A need is above the
// self floor only when that need was measured on a repeated artifact and the
// existing family sign test separates a larger discordant count.
type QuantHealth struct {
	Schema       string              `json:"schema"`
	State        string              `json:"state"`
	Reason       string              `json:"reason"`
	CacheDtype   string              `json:"cache_dtype,omitempty"`
	Self         *QuantSelfBaseline  `json:"self_baseline,omitempty"`
	Needs        []QuantNeedRow      `json:"needs,omitempty"`
	Channels     []QuantChannelCount `json:"channels,omitempty"`
	Degeneration []QuantDegeneration `json:"degeneration,omitempty"`
}

type QuantSelfBaseline struct {
	ArtifactDigest string           `json:"artifact_digest"`
	Shared         int              `json:"shared"`
	Needs          []QuantNeedFloor `json:"needs,omitempty"`
}

type QuantNeedFloor struct {
	Need               string `json:"need"`
	DiscordantFamilies int    `json:"discordant_families"`
}

type QuantNeedRow struct {
	Need           string `json:"need"`
	LeftDigest     string `json:"left_digest"`
	RightDigest    string `json:"right_digest"`
	Families       int    `json:"families"`
	LeftOnly       int    `json:"left_only"`
	RightOnly      int    `json:"right_only"`
	SelfDiscordant *int   `json:"self_discordant,omitempty"`
	AboveSelf      bool   `json:"above_self"`
}

type QuantChannelCount struct {
	ArtifactDigest string `json:"artifact_digest"`
	Need           string `json:"need"`
	Pass           int    `json:"pass"`
	Fail           int    `json:"fail"`
	Other          int    `json:"other"`
}

type QuantDegeneration struct {
	ArtifactDigest string           `json:"artifact_digest"`
	OutputHealth   string           `json:"output_health,omitempty"`
	Repetition     score.Repetition `json:"repetition"`
}

func quantHealth(results []*record.Record) QuantHealth {
	health := QuantHealth{Schema: QuantHealthSchema, State: "withheld"}
	cache, same := sameCacheDtype(results)
	if !same {
		health.Reason = cacheReason(results)
		return health
	}
	health.CacheDtype = cache
	order, groups := recordsByDigest(results)
	if len(order) != 2 {
		health.Reason = "quant health compares two artifacts"
		return health
	}
	selfDigest, leftRun, rightRun, ok := firstRepeated(order, groups)
	if !ok {
		health.Reason = "self-against-self baseline was not measured. a difference from another artifact is not separated from running the same artifact twice"
		return health
	}
	shared := eval.PairFlips(leftRun.Checks, rightRun.Checks).Shared
	if shared == 0 {
		health.Reason = "the repeated artifact shares no scorable instances, so the self-against-self noise floor is unmeasured"
		return health
	}
	health.Self = selfBaseline(selfDigest, shared, leftRun, rightRun)
	health.Needs = crossNeeds(groups[order[0]][0], groups[order[1]][0], health.Self)
	health.Channels = channelRows(results)
	health.Degeneration = degenerationRows(results)
	health.State = "descriptive"
	health.Reason = "family directions stay descriptive. a count above the self-against-self floor is a configuration difference, not a quantization attribution"
	if anyAboveSelf(health.Needs) {
		health.State = "above_self"
		health.Reason = "one need is above its self-against-self floor. that is a configuration difference, not a quantization attribution, and not a ranking"
	}
	return health
}

func sameCacheDtype(results []*record.Record) (string, bool) {
	if len(results) == 0 {
		return "", false
	}
	value, ok := observedCacheDtype(results[0])
	if !ok {
		return "", false
	}
	for _, result := range results[1:] {
		other, present := observedCacheDtype(result)
		if !present || other != value {
			return "", false
		}
	}
	return value, true
}

func cacheReason(results []*record.Record) string {
	seen := map[string]bool{}
	for _, result := range results {
		value, ok := observedCacheDtype(result)
		if !ok {
			return "cache dtype was not measured. an absent OLLAMA_KV_CACHE_TYPE is not the same configuration"
		}
		seen[value] = true
	}
	if len(seen) > 1 {
		return "cache dtype differs across the runs. it is part of the configuration"
	}
	return "cache dtype was not measured. an absent OLLAMA_KV_CACHE_TYPE is not the same configuration"
}

func observedCacheDtype(result *record.Record) (string, bool) {
	if result == nil || result.DeviceV2 == nil {
		return "", false
	}
	value := strings.TrimSpace(result.DeviceV2.Device.Config["OLLAMA_KV_CACHE_TYPE"])
	return value, value != ""
}

func recordsByDigest(results []*record.Record) ([]string, map[string][]*record.Record) {
	order := []string{}
	groups := map[string][]*record.Record{}
	for _, result := range results {
		digest := runtimeDigest(result)
		if digest == "" {
			continue
		}
		if _, ok := groups[digest]; !ok {
			order = append(order, digest)
		}
		groups[digest] = append(groups[digest], result)
	}
	return order, groups
}

func runtimeDigest(result *record.Record) string {
	if result == nil || result.Manifest == nil {
		return ""
	}
	return strings.ToLower(result.Manifest.Model.RuntimeBoundDigest())
}

func firstRepeated(order []string, groups map[string][]*record.Record) (string, *record.Record, *record.Record, bool) {
	for _, digest := range order {
		runs := groups[digest]
		if len(runs) >= 2 {
			return digest, runs[0], runs[1], true
		}
	}
	return "", nil, nil, false
}

func selfBaseline(digest string, shared int, left, right *record.Record) *QuantSelfBaseline {
	baseline := &QuantSelfBaseline{ArtifactDigest: digest, Shared: shared}
	for _, direction := range eval.NeedDirections(left.Checks, right.Checks) {
		baseline.Needs = append(baseline.Needs, QuantNeedFloor{
			Need: direction.Need, DiscordantFamilies: direction.AOnly + direction.BOnly,
		})
	}
	return baseline
}

func crossNeeds(left, right *record.Record, self *QuantSelfBaseline) []QuantNeedRow {
	floors := map[string]int{}
	for _, floor := range self.Needs {
		floors[floor.Need] = floor.DiscordantFamilies
	}
	leftDigest, rightDigest := runtimeDigest(left), runtimeDigest(right)
	rows := []QuantNeedRow{}
	for _, direction := range eval.NeedDirections(left.Checks, right.Checks) {
		row := QuantNeedRow{
			Need: direction.Need, LeftDigest: leftDigest, RightDigest: rightDigest,
			Families: direction.Shared, LeftOnly: direction.AOnly, RightOnly: direction.BOnly,
		}
		if floor, ok := floors[direction.Need]; ok {
			copied := floor
			row.SelfDiscordant = &copied
			row.AboveSelf = aboveSelfFloor(direction.AOnly, direction.BOnly, floor)
		}
		rows = append(rows, row)
	}
	return rows
}

func aboveSelfFloor(leftOnly, rightOnly, floor int) bool {
	pExact, _, separable := stats.McNemarExact(leftOnly, rightOnly)
	return separable && pExact < 0.05 && leftOnly+rightOnly > floor
}

func anyAboveSelf(rows []QuantNeedRow) bool {
	for _, row := range rows {
		if row.AboveSelf {
			return true
		}
	}
	return false
}

func channelRows(results []*record.Record) []QuantChannelCount {
	rows := []QuantChannelCount{}
	for _, result := range results {
		rows = append(rows, channelsFor(result)...)
	}
	return rows
}

func channelsFor(result *record.Record) []QuantChannelCount {
	if result == nil {
		return nil
	}
	ordered := []string{}
	counts := map[string]*QuantChannelCount{}
	for _, check := range result.Checks {
		if check.Need != "structured_output" && check.Need != "tool_calling" {
			continue
		}
		row := counts[check.Need]
		if row == nil {
			row = &QuantChannelCount{ArtifactDigest: runtimeDigest(result), Need: check.Need}
			counts[check.Need] = row
			ordered = append(ordered, check.Need)
		}
		switch check.Outcome {
		case eval.OutcomePass:
			row.Pass++
		case eval.OutcomeFail:
			row.Fail++
		case "":
			if check.Pass {
				row.Pass++
			} else {
				row.Fail++
			}
		default:
			row.Other++
		}
	}
	rows := make([]QuantChannelCount, 0, len(ordered))
	for _, need := range ordered {
		rows = append(rows, *counts[need])
	}
	return rows
}

func degenerationRows(results []*record.Record) []QuantDegeneration {
	rows := []QuantDegeneration{}
	for _, result := range results {
		if result == nil || runtimeDigest(result) == "" {
			continue
		}
		row := QuantDegeneration{ArtifactDigest: runtimeDigest(result), Repetition: result.Rep}
		if result.Scorecard.Needs != nil {
			if verdict, ok := result.Scorecard.Needs["output_health"]; ok && verdict.State != "" {
				row.OutputHealth = string(verdict.State)
			}
		}
		rows = append(rows, row)
	}
	return rows
}
