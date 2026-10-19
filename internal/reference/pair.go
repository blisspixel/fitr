package reference

import (
	"fmt"
	"sort"
	"strconv"

	"github.com/blisspixel/fitr/internal/stats"
)

// Compare pairs two derived results only when the manifest, the harness
// binding, and the seed set match. Left is the lexicographically smaller
// artifact digest. That order is identity, not a quality ranking.
func Compare(a, b Result) (Pair, error) {
	if err := a.Validate(); err != nil {
		return Pair{}, err
	}
	if err := b.Validate(); err != nil {
		return Pair{}, err
	}
	left, right := orderResults(a, b)
	pair := emptyPair(left, right)
	switch {
	case left.Plan.ManifestSHA256 != right.Plan.ManifestSHA256:
		pair.Reason = "manifest differs"
	case left.Plan.HarnessBindingSHA256 != right.Plan.HarnessBindingSHA256:
		pair.Reason = "harness binding differs"
	case left.Plan.SeedSet != right.Plan.SeedSet:
		pair.Reason = "seed set differs"
	default:
		fillPair(&pair, left, right)
	}
	return pair, nil
}

func orderResults(a, b Result) (Result, Result) {
	if a.Plan.ArtifactSHA256 <= b.Plan.ArtifactSHA256 {
		return a, b
	}
	return b, a
}

func emptyPair(left, right Result) Pair {
	return Pair{
		LeftArtifact: left.Plan.ArtifactSHA256, RightArtifact: right.Plan.ArtifactSHA256,
		FeedsFit: false, FeedsPerformance: false, FeedsRoleFloor: false, FeedsGlobalScore: false,
	}
}

func fillPair(pair *Pair, left, right Result) {
	pair.Comparable = true
	pair.Reason = "manifest and harness binding match"
	rightByKey := indexTasks(right.Tasks)
	for _, task := range left.Tasks {
		other, ok := rightByKey[taskKey(task)]
		if !ok {
			pair.Excluded++
			continue
		}
		accountPair(pair, task, other)
	}
	pair.Discordant = pair.LeftOnly + pair.RightOnly
	if pair.Discordant > 0 {
		exact, _, separable := stats.McNemarExact(pair.LeftOnly, pair.RightOnly)
		pair.Separable = separable
		if separable {
			pair.PExact = &exact
		}
	}
}

func accountPair(pair *Pair, left, right TaskResult) {
	if !scorablePair(left, right) {
		pair.Excluded++
		return
	}
	pair.Shared++
	if left.Outcome == right.Outcome {
		pair.Concordant++
		return
	}
	if left.Outcome == OutcomeResolved {
		pair.LeftOnly++
		return
	}
	pair.RightOnly++
}

func scorablePair(left, right TaskResult) bool {
	if left.Contamination != contaminationClean || right.Contamination != contaminationClean {
		return false
	}
	return scorable(left.Outcome) && scorable(right.Outcome)
}

func scorable(outcome string) bool {
	return outcome == OutcomeResolved || outcome == OutcomeUnresolved
}

func indexTasks(tasks []TaskResult) map[string]TaskResult {
	index := make(map[string]TaskResult, len(tasks))
	for _, task := range tasks {
		index[taskKey(task)] = task
	}
	return index
}

func taskKey(task TaskResult) string {
	return task.InstanceID + "#" + strconv.Itoa(task.Repeat)
}

// SeriesNotes reports one line per comparable series. More than two runs
// are not reduced to a chosen pair. A single run has no regression yet.
func SeriesNotes(results []Result) []string {
	groups := map[string][]Result{}
	var order []string
	for _, result := range results {
		key := result.Plan.ManifestSHA256 + "\n" + result.Plan.HarnessBindingSHA256 + "\n" + result.Plan.SeedSet
		if _, ok := groups[key]; !ok {
			order = append(order, key)
		}
		groups[key] = append(groups[key], result)
	}
	var notes []string
	for _, key := range order {
		notes = append(notes, seriesNote(groups[key])...)
	}
	return notes
}

func seriesNote(group []Result) []string {
	if len(group) > 2 {
		return []string{fmt.Sprintf(
			"external reference: %d runs share one manifest; name two files to compare", len(group))}
	}
	if len(group) != 2 {
		return nil
	}
	sort.Slice(group, func(i, j int) bool {
		return group[i].Plan.ArtifactSHA256 < group[j].Plan.ArtifactSHA256
	})
	pair, err := Compare(group[0], group[1])
	if err != nil || !pair.Comparable {
		return []string{"external reference: the saved files are not a comparable pair"}
	}
	return []string{FormatPair(pair)}
}

// FormatPair is the one sentence board and history show. It names the
// discordant counts and withholds a winner.
func FormatPair(pair Pair) string {
	if !pair.Comparable {
		return "external reference: not comparable: " + pair.Reason
	}
	text := fmt.Sprintf(
		"external reference %s vs %s: %d discordant (%d left-only, %d right-only), %d excluded",
		shortDigest(pair.LeftArtifact), shortDigest(pair.RightArtifact),
		pair.Discordant, pair.LeftOnly, pair.RightOnly, pair.Excluded)
	switch {
	case pair.Discordant == 0:
		text += "; the pair did not separate"
	case !pair.Separable:
		text += "; too few discordant pairs to separate at alpha 0.05"
	}
	return text + "; not a fit or performance claim"
}

func shortDigest(value string) string {
	const prefix = "sha256:"
	if len(value) < len(prefix)+8 {
		return value
	}
	return value[len(prefix) : len(prefix)+8]
}
