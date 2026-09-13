package source

import (
	"encoding/json"
	"errors"
	"strings"
	"time"
	"unicode"
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
// BaseAuthor and FirstPartyConversion are arithmetic over the provider's
// declarations: every base has one common author, and that author matches this
// repository's. This is not a quality claim. A third-party conversion is
// frequently the only one that exists, and is not thereby worse.
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
	relation := "published by " + p.Author + "; declares derivation from " + strings.Join(p.BaseModels, ", ")
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
		Author: strings.TrimSpace(meta.Author), License: publisherText(decodePublisherString(meta.CardData.License), 256),
		Gated: decodeGated(meta.Gated), CreatedAt: publisherText(meta.CreatedAt, 64),
		BaseModels: decodeBaseModels(meta.CardData.BaseModel),
	}
	owner, _, _ := strings.Cut(meta.ID, "/")
	switch {
	case !validRepo(meta.ID):
		p.Author = ""
	case p.Author == "":
		p.Author = owner
	case !strings.EqualFold(p.Author, owner):
		// A conflicting declaration cannot impersonate the base publisher.
		// Keep the disagreement unmeasured instead of silently preferring it.
		p.Author = ""
	}
	if p.CreatedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, p.CreatedAt); err != nil {
			p.CreatedAt = ""
		}
	}
	// Checked against the Hugging Face Hub model-card documentation on
	// 2026-09-13: base_model may be a list for merges, and the relationship
	// can be explicit even when the provider did not emit derived tags.
	// https://huggingface.co/docs/hub/model-cards#specifying-a-base-model
	p.DeclaredQuantization = decodePublisherString(meta.CardData.BaseModelRelation) == "quantized"
	for _, tag := range meta.Tags {
		if strings.HasPrefix(tag, "base_model:quantized:") || strings.HasPrefix(tag, "base_model:gguf:") {
			p.DeclaredQuantization = true
		}
	}
	p.BaseAuthor, p.FirstPartyConversion = publisherLineage(p.Author, p.BaseModels)
	if p.Author == "" && !p.Lineage() && p.License == "" && p.Gated == "" && p.CreatedAt == "" && !p.DeclaredQuantization {
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
		if single = strings.TrimSpace(single); validRepo(single) {
			return []string{single}
		}
		return nil
	}
	var many []string
	if json.Unmarshal(raw, &many) != nil || len(many) > MaxFiles {
		return nil
	}
	var out []string
	for _, entry := range many {
		entry = strings.TrimSpace(entry)
		if !validRepo(entry) {
			// Dropping one unknown parent can turn a mixed merge into a
			// first-party artifact, so the entire declaration stays missing.
			return nil
		}
		out = append(out, entry)
	}
	return out
}

// decodeGated accepts the boolean and the named forms. A gated repository can
// still answer metadata while refusing the files, so this is recorded rather
// than treated as a failure. ModelInfo documents auto/manual/False in the
// Hugging Face Hub API reference, checked 2026-09-13; true remains accepted for
// existing wire fixtures without inventing an approval mode.
func decodeGated(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var named string
	if json.Unmarshal(raw, &named) == nil {
		switch strings.TrimSpace(named) {
		case "auto", "manual":
			return strings.TrimSpace(named)
		}
		return ""
	}
	var flag bool
	if json.Unmarshal(raw, &flag) == nil && flag {
		return "yes"
	}
	return ""
}

func decodePublisherString(raw json.RawMessage) string {
	var value string
	if json.Unmarshal(raw, &value) != nil {
		return ""
	}
	return strings.TrimSpace(value)
}

func publisherText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if len(value) > limit || strings.ContainsFunc(value, unicode.IsControl) {
		return ""
	}
	return value
}

func validPublisherOwner(value string) bool {
	return validRepo(value + "/model")
}

func publisherLineage(author string, bases []string) (string, bool) {
	if len(bases) == 0 {
		return "", false
	}
	common, _, _ := strings.Cut(bases[0], "/")
	for _, base := range bases {
		owner, _, _ := strings.Cut(base, "/")
		if !validRepo(base) || !strings.EqualFold(common, owner) {
			return "", false
		}
	}
	return common, strings.EqualFold(common, author)
}

func (p *Publisher) validate() error {
	if p == nil {
		return nil
	}
	if p.Author != "" && !validPublisherOwner(p.Author) {
		return errors.New("invalid source publisher author")
	}
	if len(p.BaseModels) > MaxFiles {
		return errors.New("source publisher has too many base models")
	}
	for _, base := range p.BaseModels {
		if !validRepo(base) {
			return errors.New("invalid source publisher base model")
		}
	}
	baseAuthor, firstParty := publisherLineage(p.Author, p.BaseModels)
	if p.BaseAuthor != baseAuthor || p.FirstPartyConversion != firstParty {
		return errors.New("source publisher lineage contradicts its declared base models")
	}
	if publisherText(p.License, 256) != p.License || publisherText(p.CreatedAt, 64) != p.CreatedAt {
		return errors.New("invalid source publisher metadata text")
	}
	if p.Gated != "" && p.Gated != "yes" && p.Gated != "auto" && p.Gated != "manual" {
		return errors.New("invalid source publisher gating state")
	}
	if p.CreatedAt != "" {
		if _, err := time.Parse(time.RFC3339Nano, p.CreatedAt); err != nil {
			return errors.New("invalid source publisher creation timestamp")
		}
	}
	return nil
}
