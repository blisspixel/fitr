package mcp

import (
	"context"
	"encoding/json"
	"regexp"
)

var roleNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)

func (s *server) call(ctx context.Context, req request) response {
	var name string
	if !onlyKeys(req.params, "_meta", "name", "arguments") ||
		json.Unmarshal(req.params["name"], &name) != nil || name == "" {
		return failure(req.id, -32602, "Malformed tool call; continuations are not supported")
	}
	arguments := object{}
	if raw, exists := req.params["arguments"]; exists {
		var ok bool
		if arguments, ok = decodeObject(raw); !ok {
			return failure(req.id, -32602, "Tool arguments must be an object")
		}
	}
	var data any
	var err error
	switch name {
	case "fitr_roles_list":
		if len(arguments) != 0 {
			return s.toolError(req.id, "This tool accepts an empty argument object.")
		}
		data, err = s.source.list(ctx)
	case "fitr_role_review", "fitr_role_status":
		var roleName string
		if !onlyKeys(arguments, "role") || json.Unmarshal(arguments["role"], &roleName) != nil || !roleNamePattern.MatchString(roleName) {
			return s.toolError(req.id, "Provide role as 1 to 64 lowercase letters, digits or hyphens, starting with a letter or digit.")
		}
		if name == "fitr_role_status" {
			data, err = s.source.status(ctx, roleName)
		} else {
			data, err = s.source.review(ctx, roleName)
		}
	default:
		return failure(req.id, -32602, "Unknown tool")
	}
	if err != nil {
		if name == "fitr_role_status" {
			return s.toolError(req.id, "Local selection evidence is unavailable, invalid or exceeds this profile's limits. Inspect it with fitr role status locally.")
		}
		return s.toolError(req.id, "Local evidence is unavailable, invalid or exceeds this profile's limits. Inspect it with fitr role review locally.")
	}
	encoded, err := json.Marshal(data)
	if err != nil || len(encoded) > maxMessageBytes {
		return failure(req.id, -32603, "Evidence response could not be encoded within its limit")
	}
	return s.complete(req.id, map[string]any{
		"content":           []map[string]string{{"type": "text", "text": string(encoded)}},
		"structuredContent": data, "isError": false,
	})
}

func (s *server) toolError(id json.RawMessage, message string) response {
	return s.complete(id, map[string]any{"content": []map[string]string{{"type": "text", "text": message}}, "isError": true})
}

func catalog() []map[string]any {
	annotations := map[string]bool{"readOnlyHint": true, "destructiveHint": false, "idempotentHint": true, "openWorldHint": false}
	return []map[string]any{
		{"name": "fitr_role_review", "description": "Recheck a local role's canonical battery screening evidence. Returns redacted states and preference bounds; never authorizes model adoption.",
			"inputSchema":  objectSchema(map[string]any{"role": map[string]any{"type": "string", "pattern": roleNamePattern.String(), "maxLength": 64}}, "role"),
			"outputSchema": reviewSchema(), "annotations": annotations},
		{"name": "fitr_role_status", "description": "Recheck an existing local role selection against its lifecycle and canonical or closed managed evidence. Returns only redacted qualification, digests and expiry; never authorizes execution or adoption.",
			"inputSchema":  objectSchema(map[string]any{"role": map[string]any{"type": "string", "pattern": roleNamePattern.String(), "maxLength": 64}}, "role"),
			"outputSchema": statusSchema(), "annotations": annotations},
		{"name": "fitr_roles_list", "description": "List local role names, revision digests and attachment counts. No model names, descriptions, source paths or raw evidence are shared.",
			"inputSchema": objectSchema(map[string]any{}), "outputSchema": listSchema(), "annotations": annotations},
	}
}

func objectSchema(properties map[string]any, required ...string) map[string]any {
	schema := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		schema["required"] = required
	}
	return schema
}

func stringSchema() map[string]any { return map[string]any{"type": "string"} }
func countSchema() map[string]any  { return map[string]any{"type": "integer", "minimum": 0} }
func arraySchema(items map[string]any) map[string]any {
	return map[string]any{"type": "array", "items": items, "maxItems": 64}
}

func listSchema() map[string]any {
	entry := objectSchema(map[string]any{"name": stringSchema(), "revision": stringSchema(), "candidate_count": countSchema()}, "name", "revision", "candidate_count")
	return objectSchema(map[string]any{"schema": map[string]any{"const": "fitr.mcp.roles.v1"}, "roles": arraySchema(entry)}, "schema", "roles")
}

func reviewSchema() map[string]any {
	bound := map[string]any{"type": "number", "minimum": 0, "maximum": 1}
	preference := objectSchema(map[string]any{"estimate": bound, "low": bound, "high": bound}, "estimate", "low", "high")
	candidate := objectSchema(map[string]any{"evidence_sha256": stringSchema(), "state": stringSchema(), "reason_count": countSchema(), "preference": preference}, "evidence_sha256", "state", "reason_count")
	return objectSchema(map[string]any{
		"schema": map[string]any{"const": "fitr.mcp.review.v1"}, "role": stringSchema(), "revision": stringSchema(),
		"scope": map[string]any{"const": "battery_screening"}, "state": stringSchema(), "evaluated_at": stringSchema(),
		"candidates": arraySchema(candidate), "exploration_lead": stringSchema(), "gap_count": countSchema(),
		"comparison_ready": map[string]any{"type": "boolean"}, "adoption_authorized": map[string]any{"const": false},
	}, "schema", "role", "revision", "scope", "state", "evaluated_at", "candidates", "gap_count", "comparison_ready", "adoption_authorized")
}
