// Package mcp implements fitr's read-only MCP 2026-07-28 stdio profile.
// It deliberately has no HTTP transport, execution tools, or legacy handshake.
package mcp

import (
	"bytes"
	"encoding/json"
	"regexp"
	"strconv"
	"unicode/utf8"

	"github.com/blisspixel/fitr/internal/strictjson"
)

const ProtocolVersion = "2026-07-28"

const (
	versionKey      = "io.modelcontextprotocol/protocolVersion"
	capabilitiesKey = "io.modelcontextprotocol/clientCapabilities"
	clientInfoKey   = "io.modelcontextprotocol/clientInfo"
	maxMessageBytes = 64 << 10
)

type object = map[string]json.RawMessage

type request struct {
	id     json.RawMessage
	key    string
	method string
	params object
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

type response struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Result  map[string]any  `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

func failure(id json.RawMessage, code int, message string) response {
	return response{JSONRPC: "2.0", ID: id, Error: &rpcError{Code: code, Message: message}}
}

func decodeRequest(data []byte) (request, *response) {
	if !utf8.Valid(data) || !json.Valid(data) {
		r := failure(nil, -32700, "Invalid JSON")
		return request{}, &r
	}
	fields, ok := decodeObject(data)
	if !ok || strictjson.Validate(data) != nil {
		r := failure(nil, -32600, "Invalid request")
		return request{}, &r
	}
	req := request{id: fields["id"]}
	if req.id != nil {
		if req.key, ok = idKey(req.id); !ok {
			r := failure(nil, -32600, "Invalid request ID")
			return request{}, &r
		}
	}
	var rpc string
	if json.Unmarshal(fields["jsonrpc"], &rpc) != nil || rpc != "2.0" ||
		json.Unmarshal(fields["method"], &req.method) != nil || req.method == "" || len(req.method) > 128 {
		r := failure(req.id, -32600, "Invalid request")
		return request{}, &r
	}
	if !onlyKeys(fields, "jsonrpc", "id", "method", "params") {
		if req.id == nil {
			return request{}, nil
		}
		r := failure(req.id, -32600, "Invalid request")
		return request{}, &r
	}
	if raw, exists := fields["params"]; exists {
		if req.params, ok = decodeObject(raw); !ok {
			r := failure(req.id, -32602, "Parameters must be an object")
			if req.id == nil {
				return req, nil
			}
			return req, &r
		}
	}
	return req, nil
}

func decodeObject(data []byte) (object, bool) {
	var result object
	err := json.Unmarshal(data, &result)
	return result, err == nil && result != nil
}

func onlyKeys(fields object, allowed ...string) bool {
	for key := range fields {
		found := false
		for _, name := range allowed {
			found = found || key == name
		}
		if !found {
			return false
		}
	}
	return true
}

func idKey(raw json.RawMessage) (string, bool) {
	raw = bytes.TrimSpace(raw)
	if bytes.Equal(raw, []byte("null")) {
		return "", false
	}
	var value string
	if json.Unmarshal(raw, &value) == nil {
		return "s:" + value, len(value) <= 128
	}
	number, err := strconv.ParseInt(string(raw), 10, 64)
	return "n:" + strconv.FormatInt(number, 10), err == nil
}

var metaKeyPattern = regexp.MustCompile(`^(?:[a-zA-Z](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?(?:\.[a-zA-Z](?:[a-zA-Z0-9-]*[a-zA-Z0-9])?)*/)?(?:[a-zA-Z0-9](?:[a-zA-Z0-9_.-]*[a-zA-Z0-9])?)?$`)

func validateMetadata(req request) *response {
	meta, ok := decodeObject(req.params["_meta"])
	var requested string
	_, capabilitiesOK := decodeObject(meta[capabilitiesKey])
	if !ok || !capabilitiesOK || json.Unmarshal(meta[versionKey], &requested) != nil ||
		bytes.Equal(meta[versionKey], []byte("null")) || len(requested) > 128 || !validMetaFields(meta) {
		r := failure(req.id, -32602, "Required request metadata is missing or invalid")
		return &r
	}
	if requested != ProtocolVersion {
		r := failure(req.id, -32022, "Unsupported protocol version")
		r.Error.Data = map[string]any{"supported": []string{ProtocolVersion}, "requested": requested}
		return &r
	}
	return nil
}

func validMetaFields(meta object) bool {
	for key := range meta {
		if !metaKeyPattern.MatchString(key) {
			return false
		}
	}
	if raw, exists := meta[clientInfoKey]; exists {
		info, ok := decodeObject(raw)
		if !ok {
			return false
		}
		for _, key := range []string{"name", "version"} {
			var value string
			if json.Unmarshal(info[key], &value) != nil || bytes.Equal(info[key], []byte("null")) {
				return false
			}
		}
	}
	return validCapabilityShapes(meta[capabilitiesKey])
}

// No optional capability is used by this profile. Still reject malformed known
// capability containers rather than silently assigning them protocol meaning.
func validCapabilityShapes(raw json.RawMessage) bool {
	capabilities, ok := decodeObject(raw)
	if !ok {
		return false
	}
	for _, key := range []string{"roots", "sampling", "elicitation", "experimental", "extensions"} {
		if value, exists := capabilities[key]; exists {
			settings, ok := decodeObject(value)
			if !ok {
				return false
			}
			if key == "extensions" || key == "experimental" {
				for _, entry := range settings {
					if _, ok := decodeObject(entry); !ok {
						return false
					}
				}
			}
		}
	}
	return true
}
