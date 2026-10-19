package reference

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"strings"
)

func hashValue(domain string, value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(append([]byte(domain+"\x00"), data...))
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func validSHA256(value string) bool {
	encoded, ok := strings.CutPrefix(value, "sha256:")
	if !ok || len(encoded) != sha256.Size*2 || encoded != strings.ToLower(encoded) {
		return false
	}
	_, err := hex.DecodeString(encoded)
	return err == nil
}
