package reference

import (
	"errors"
	"regexp"
	"time"
)

var (
	instanceIDPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._-]{0,80}$`)
	namePattern       = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,63}$`)
	versionPattern    = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)
	runtimePattern    = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,31}$`)
	seedPattern       = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

type manifestBody struct {
	Dataset         string     `json:"dataset"`
	DatasetRevision string     `json:"dataset_revision"`
	DatasetSplit    string     `json:"dataset_split"`
	SubsetID        string     `json:"subset_id"`
	Instances       []Instance `json:"instances"`
}

type harnessBody struct {
	Name            string `json:"harness_name"`
	Version         string `json:"harness_version"`
	ConfigSHA256    string `json:"harness_config_sha256"`
	StepLimit       int    `json:"step_limit"`
	TaskWallSeconds int    `json:"task_wall_seconds"`
	ContextCap      int    `json:"context_cap"`
	K               int    `json:"k"`
}

// NewPlan seals the frozen subset. Digests already on the draft are replaced,
// so a caller cannot supply a friendlier manifest than the instances.
func NewPlan(draft Plan) (Plan, error) {
	draft.Schema = PlanSchema
	draft.PlanSHA256 = ""
	draft.ManifestSHA256 = ""
	draft.HarnessBindingSHA256 = ""
	if err := validateDraft(draft); err != nil {
		return Plan{}, err
	}
	return sealPlan(draft)
}

func sealPlan(draft Plan) (Plan, error) {
	manifest, err := hashValue(manifestDomain, manifestOf(draft))
	if err != nil {
		return Plan{}, err
	}
	harness, err := hashValue(harnessDomain, harnessOf(draft))
	if err != nil {
		return Plan{}, err
	}
	draft.ManifestSHA256 = manifest
	draft.HarnessBindingSHA256 = harness
	digest, err := hashValue(PlanSchema, draft)
	if err != nil {
		return Plan{}, err
	}
	draft.PlanSHA256 = digest
	return draft, nil
}

// Validate recomputes the manifest, the harness binding, and the plan
// digest. A stored conclusion that does not match those bytes is rejected.
func (plan Plan) Validate() error {
	if plan.Schema != PlanSchema {
		return errors.New("unsupported external reference plan schema")
	}
	cleared := plan
	cleared.PlanSHA256 = ""
	if err := validateDraft(cleared); err != nil {
		return err
	}
	sealed, err := sealPlan(cleared)
	if err != nil {
		return err
	}
	if sealed.ManifestSHA256 != plan.ManifestSHA256 {
		return errors.New("external reference manifest digest does not match")
	}
	if sealed.HarnessBindingSHA256 != plan.HarnessBindingSHA256 {
		return errors.New("external reference harness binding does not match")
	}
	if sealed.PlanSHA256 != plan.PlanSHA256 {
		return errors.New("external reference plan digest does not match")
	}
	return nil
}

func manifestOf(plan Plan) manifestBody {
	return manifestBody{
		Dataset: plan.Dataset, DatasetRevision: plan.DatasetRevision,
		DatasetSplit: plan.DatasetSplit, SubsetID: plan.SubsetID,
		Instances: plan.Instances,
	}
}

func harnessOf(plan Plan) harnessBody {
	return harnessBody{
		Name: plan.HarnessName, Version: plan.HarnessVersion,
		ConfigSHA256: plan.HarnessConfigSHA256, StepLimit: plan.StepLimit,
		TaskWallSeconds: plan.TaskWallSeconds, ContextCap: plan.ContextCap, K: plan.K,
	}
}

func validateDraft(plan Plan) error {
	if err := validateIdentity(plan); err != nil {
		return err
	}
	if err := validateBounds(plan); err != nil {
		return err
	}
	return validateInstances(plan.Instances)
}

func validateIdentity(plan Plan) error {
	if plan.Dataset != DatasetID || plan.DatasetRevision != DatasetRevision ||
		plan.DatasetSplit != DatasetSplit || plan.SubsetID != SubsetID {
		return errors.New("external reference plan must use the frozen subset")
	}
	if !namePattern.MatchString(plan.HarnessName) || !versionPattern.MatchString(plan.HarnessVersion) {
		return errors.New("harness name or version is not a sealed identifier")
	}
	if !validSHA256(plan.HarnessConfigSHA256) || !validSHA256(plan.ArtifactSHA256) || !validSHA256(plan.DeviceKey) {
		return errors.New("a sealed digest is missing or is not lowercase sha256")
	}
	if !seedPattern.MatchString(plan.SeedSet) || !validDate(plan.ArtifactRelease) {
		return errors.New("seed set or artifact release date is invalid")
	}
	if !versionPattern.MatchString(plan.Quant) || !runtimePattern.MatchString(plan.Runtime) ||
		!versionPattern.MatchString(plan.RuntimeBuild) {
		return errors.New("quant, runtime, or runtime build is not a sealed identifier")
	}
	return nil
}

func validateBounds(plan Plan) error {
	if plan.K < 1 || plan.K > maxK || plan.StepLimit < 1 || plan.StepLimit > maxSteps {
		return errors.New("k or step limit is outside the sealed range")
	}
	if plan.TaskWallSeconds < 1 || plan.TaskWallSeconds > maxTaskWall {
		return errors.New("task wall cap is outside the sealed range")
	}
	if plan.ContextCap < 1 || plan.ContextCap > maxContext {
		return errors.New("context cap is outside the sealed range")
	}
	if plan.WallBudgetSeconds < 1 || plan.WallBudgetSeconds > maxWallBudget {
		return errors.New("wall budget is outside the sealed range")
	}
	if plan.DiskBudgetBytes < 1 || plan.DiskBudgetBytes > maxDisk ||
		plan.RequiredDiskBytes < 1 || plan.RequiredDiskBytes > maxDisk {
		return errors.New("disk budget is outside the sealed range")
	}
	if plan.PrefillTokensPerSecond < 1 || plan.PrefillTokensPerSecond > maxRate ||
		plan.DecodeTokensPerSecond < 1 || plan.DecodeTokensPerSecond > maxRate {
		return errors.New("a measured token rate is missing or outside the sealed range")
	}
	if plan.PromptTokens < 1 || plan.PromptTokens > maxTokens ||
		plan.CompletionTokens < 1 || plan.CompletionTokens > maxTokens {
		return errors.New("the sealed token estimate is outside the sealed range")
	}
	return nil
}

func validateInstances(instances []Instance) error {
	subset := DefaultSubset()
	if len(instances) != len(subset) {
		return errors.New("external reference plan does not use the frozen subset")
	}
	for i, instance := range instances {
		if instance.ID != subset[i].ID || instance.Created != subset[i].Created {
			return errors.New("external reference plan does not use the frozen subset")
		}
		if !instanceIDPattern.MatchString(instance.ID) || !validDate(instance.Created) ||
			!validSHA256(instance.ImageDigest) {
			return errors.New("an instance identity or image digest is invalid")
		}
	}
	return nil
}

func validDate(value string) bool {
	parsed, err := time.Parse(time.DateOnly, value)
	return err == nil && parsed.Format(time.DateOnly) == value
}
