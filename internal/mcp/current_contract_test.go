package mcp

import (
	"bytes"
	"io"
	"strings"
	"testing"
)

// These nested object shapes and extension namespace rules were checked at
// modelcontextprotocol/modelcontextprotocol cc2a84f5ca5404b2949683f7d7876f623344294f,
// schema/2026-07-28/schema.ts. Optional capabilities never grant this profile
// authority, but malformed declarations must not silently become valid ones.
func TestCurrentCapabilityShapesRejectMalformedNestedSettings(t *testing.T) {
	for _, value := range []string{
		`{"extensions":{"unprefixed":{}}}`, `{"extensions":{"bad namespace/name":{}}}`,
		`{"sampling":{"tools":true}}`, `{"sampling":{"context":null}}`,
		`{"elicitation":{"form":[]}}`, `{"elicitation":{"url":"yes"}}`,
	} {
		input := strings.Replace(testRequest("server/discover", ""), `"io.modelcontextprotocol/clientCapabilities":{}`,
			`"io.modelcontextprotocol/clientCapabilities":`+value, 1)
		req, invalid := decodeRequest([]byte(input))
		if invalid != nil || validateMetadata(req) == nil {
			t.Errorf("accepted malformed capability declaration: %s", value)
		}
	}
}

func TestLegacyClientFailureNamesSupportedModernVersion(t *testing.T) {
	for _, input := range []string{`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`, testRequest("initialize", "")} {
		var output bytes.Buffer
		if err := serve(t.Context(), io.NopCloser(strings.NewReader(input+"\n")), &output, fixtureSource{}, "fixture"); err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(output.String(), `"error"`) || !strings.Contains(output.String(), "2026-07-28") {
			t.Errorf("legacy-only client received no supported-version diagnostic: %s", output.String())
		}
	}
}

func TestCurrentOptionalCapabilitiesDoNotAddAuthority(t *testing.T) {
	input := strings.Replace(testRequest("server/discover", ""), `"io.modelcontextprotocol/clientCapabilities":{}`,
		`"io.modelcontextprotocol/clientCapabilities":{"extensions":{"io.modelcontextprotocol/ui":{"mimeTypes":["text/html;profile=mcp-app"]}},"sampling":{"tools":{}},"elicitation":{"form":{}},"org.example.future":["unknown"]}`, 1)
	req, invalid := decodeRequest([]byte(input))
	if invalid != nil || validateMetadata(req) != nil {
		t.Fatal("valid optional or additive capabilities were refused")
	}
	s := server{version: "fixture"}
	reply := s.protocol(req)
	capabilities := reply.Result["capabilities"].(map[string]any)
	if len(capabilities) != 1 || capabilities["tools"] == nil {
		t.Fatalf("client capabilities changed server authority: %+v", capabilities)
	}
}
