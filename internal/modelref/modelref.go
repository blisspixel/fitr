// Package modelref defines identity rules shared by runtime and saved-model
// selection. It deliberately avoids partial or fuzzy matching.
package modelref

import "strings"

// SameServed treats a tag and its optional :latest spelling as the same model.
// All other names remain distinct.
func SameServed(want, have string) bool {
	return ServedKey(want) == ServedKey(have)
}

// ServedKey removes one case-insensitive default tag using SameServed's rules.
func ServedKey(value string) string {
	const suffix = ":latest"
	if len(value) >= len(suffix) && strings.EqualFold(value[len(value)-len(suffix):], suffix) {
		return value[:len(value)-len(suffix)]
	}
	return value
}
