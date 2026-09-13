package source

import (
	"encoding/json"
	"strings"
)

// Publisher records who produced an artifact and what it was produced from.
//
// It exists because the obvious way to pick a candidate is the wrong one.
// Download counts rank a repository by how long it has existed and how well it
// is known, which is exactly backwards for the new releases someone is asking
// about, and in practice they promote third-party requantizations above the
// model author's own conversion. Lineage answers the same question without
// that bias: who converted this, and from whose model.
//
// Every field here is copied from the provider's metadata. FirstPartyConversion
// is the one derived value and it is arithmetic rather than judgement: the
// author of this repository is the author of the base model. It is not a
// quality claim. A third-party conversion is frequently the only one that
// exists, and is not thereby worse.
type Publisher struct {
	Author               string   `json:"author,omitempty"`
	BaseModels           []string `json:"base_models,omitempty"`
	BaseAuthor           string   `json:"base_author,omitempty"`
	FirstPartyConversion bool     `json:"first_party_conversion"`
	DeclaredQuantization bool     `json:"declared_quantization,omitempty"`
	License              string   `json:"license,omitempty"`
	Gated                string   `json:"gated,omitempty"`
	CreatedAt            string   `json:"created_at,omitempty"`
}

// Lineage reports whether the base model is known at all. Absent lineage is not
// evidence of a first-party artifact; it is evidence of nothing.
func (p *Publisher) Lineage() bool { return p != nil && len(p.BaseModels) > 0 }

// Summary is one line for a terminal card, or empty when the provider said
// nothing worth reporting.
func (p *Publisher) Summary() string {
	if p == nil || p.Author == "" {
		return ""
	}
	if !p.Lineage() {
		return p.Author + "; the repository declares no base model, so its lineage is unknown"
	}
	relation := "converted by " + p.Author + " from " + strings.Join(p.BaseModels, ", ")
	if p.FirstPartyConversion {
		relation = "published by " + p.Author + ", the author of " + strings.Join(p.BaseModels, ", ")
	}
	if p.DeclaredQuantization {
		relation += "; declared a quantization"
	}
	return relation
}

// publisherFrom reads the lineage fields out of one already-fetched metadata
// document. It costs no additional request: the resolver has this response in
// hand for the file metadata it came for.
func publisherFrom(meta hfMetadata) *Publisher {
	p := &Publisher{
		Author: strings.TrimSpace(meta.Author), License: strings.TrimSpace(meta.CardData.License),
		Gated: decodeGated(meta.Gated), CreatedAt: strings.TrimSpace(meta.CreatedAt),
		BaseModels: decodeBaseModels(meta.CardData.BaseModel),
	}
	if p.Author == "" {
		// The repository id carries the owner when the field is absent.
		if owner, _, found := strings.Cut(meta.ID, "/"); found {
			p.Author = owner
		}
	}
	for _, tag := range meta.Tags {
		if strings.HasPrefix(tag, "base_model:quantized:") || strings.HasPrefix(tag, "base_model:gguf:") {
			p.DeclaredQuantization = true
		}
	}
	if len(p.BaseModels) > 0 {
		if owner, _, found := strings.Cut(p.BaseModels[0], "/"); found {
			p.BaseAuthor = owner
		}
		p.FirstPartyConversion = p.BaseAuthor != "" &&
			strings.EqualFold(p.BaseAuthor, p.Author)
	}
	if p.Author == "" && !p.Lineage() && p.License == "" {
		return nil
	}
	return p
}

// decodeBaseModels accepts the two shapes the field takes in practice: one
// string, or a list of them. Anything else is no lineage rather than a guess.
func decodeBaseModels(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		if single = strings.TrimSpace(single); single != "" {
			return []string{single}
		}
		return nil
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil {
		return nil
	}
	var out []string
	for _, entry := range many {
		if entry = strings.TrimSpace(entry); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// decodeGated accepts the boolean and the named forms. A gated repository can
// still answer metadata while refusing the files, so this is recorded rather
// than treated as a failure.
func decodeGated(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var named string
	if json.Unmarshal(raw, &named) == nil {
		return strings.TrimSpace(named)
	}
	var flag bool
	if json.Unmarshal(raw, &flag) == nil && flag {
		return "yes"
	}
	return ""
}
