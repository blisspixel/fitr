package decision

import "sort"

// RequalificationSchema is the derived installation comparison. It is not
// a sealed run manifest and it is not a quality pass.
const RequalificationSchema = "fitr.requalification.v1"

const (
	FactObserved  = "observed"
	FactRequested = "requested"
	FactUnknown   = "unknown"
)

const (
	RequalifyValid      = "valid"
	RequalifyStale      = "stale"
	RequalifyUnresolved = "unresolved"
)

// RequalifyFreshConfirmation names the existing fresh-confirmation path.
// It is not a command to rerun every historical claim.
const RequalifyFreshConfirmation = "fresh-confirmation"

// IdentityFact is one installation input. Requested and unknown are not
// observations of the installed stack, and an empty state is unknown.
type IdentityFact struct {
	State string
	Value string
}

// InstallationIdentity is the stack a qualification may be reused against.
// ModelName and Announcement are not identity. Presentation is not a
// configuration input.
type InstallationIdentity struct {
	ArtifactDigest   IdentityFact
	Quant            IdentityFact
	Runtime          IdentityFact
	ComputeBackend   IdentityFact
	Driver           IdentityFact
	OS               IdentityFact
	Kernel           IdentityFact
	Hardware         IdentityFact
	EffectiveContext IdentityFact
	Concurrency      IdentityFact
	Residency        IdentityFact
	Harness          IdentityFact
	Presentation     string
	ModelName        string
	Announcement     string
}

// RequalificationClaimInput is one historical claim and the identity
// fields it depends on. An empty dependency list is unknown coverage.
type RequalificationClaimInput struct {
	ID        string
	DependsOn []string
}

// RequalificationReport says which claims still apply. It does not modify
// the historical receipt.
type RequalificationReport struct {
	Schema string                 `json:"schema"`
	Claims []RequalifiedClaim     `json:"claims"`
	Next   *RequalificationAction `json:"next_action,omitempty"`
}

// RequalifiedClaim is one claim after the identity comparison.
type RequalifiedClaim struct {
	ID      string   `json:"id"`
	State   string   `json:"state"`
	Changed []string `json:"changed,omitempty"`
	Unknown []string `json:"unknown,omitempty"`
}

// RequalificationAction names only the claims that need fresh
// confirmation. It is not a full rerun.
type RequalificationAction struct {
	Code   string   `json:"code"`
	Claims []string `json:"claims"`
	Reason string   `json:"reason"`
}

// Requalify compares one historical installation with the current one.
// It does not read the host, does not submit work, and does not erase the
// historical receipt. A model name and a release announcement are ignored.
func Requalify(prior, current InstallationIdentity, claims []RequalificationClaimInput) RequalificationReport {
	out := RequalificationReport{Schema: RequalificationSchema}
	var affected []string
	for _, claim := range claims {
		classified := classifyRequalification(prior, current, claim)
		out.Claims = append(out.Claims, classified)
		if classified.State != RequalifyValid {
			affected = append(affected, classified.ID)
		}
	}
	if len(affected) == 0 {
		return out
	}
	out.Next = &RequalificationAction{
		Code:   RequalifyFreshConfirmation,
		Claims: affected,
		Reason: "fresh confirmation of the claims whose installation identity changed or is unobserved",
	}
	return out
}

func classifyRequalification(prior, current InstallationIdentity, claim RequalificationClaimInput) RequalifiedClaim {
	got := RequalifiedClaim{ID: claim.ID}
	if len(claim.DependsOn) == 0 {
		got.State = RequalifyUnresolved
		got.Unknown = []string{"dependency-coverage"}
		return got
	}
	left := identityFields(prior)
	right := identityFields(current)
	var changed, unknown []string
	for _, name := range claim.DependsOn {
		priorFact, priorOK := left[name]
		currentFact, currentOK := right[name]
		if !priorOK || !currentOK || !factObserved(priorFact) || !factObserved(currentFact) {
			unknown = addRequalifyName(unknown, name)
			continue
		}
		if priorFact.Value != currentFact.Value {
			changed = addRequalifyName(changed, name)
		}
	}
	sort.Strings(changed)
	sort.Strings(unknown)
	got.Changed = changed
	got.Unknown = unknown
	switch {
	case len(changed) > 0:
		got.State = RequalifyStale
	case len(unknown) > 0:
		got.State = RequalifyUnresolved
	default:
		got.State = RequalifyValid
	}
	return got
}

func factObserved(fact IdentityFact) bool {
	return fact.State == FactObserved
}

func addRequalifyName(names []string, name string) []string {
	for _, have := range names {
		if have == name {
			return names
		}
	}
	return append(names, name)
}

func identityFields(id InstallationIdentity) map[string]IdentityFact {
	return map[string]IdentityFact{
		"artifact_digest":   id.ArtifactDigest,
		"quant":             id.Quant,
		"runtime":           id.Runtime,
		"compute_backend":   id.ComputeBackend,
		"driver":            id.Driver,
		"os":                id.OS,
		"kernel":            id.Kernel,
		"hardware":          id.Hardware,
		"effective_context": id.EffectiveContext,
		"concurrency":       id.Concurrency,
		"residency":         id.Residency,
		"harness":           id.Harness,
	}
}
