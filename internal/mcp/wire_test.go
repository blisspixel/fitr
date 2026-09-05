package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"
)

const fixtureMeta = `"_meta":{"io.modelcontextprotocol/protocolVersion":"2026-07-28","io.modelcontextprotocol/clientCapabilities":{}}`

type fixtureSource struct {
	result any
	err    error
}

func (f fixtureSource) list(context.Context) (any, error) {
	if f.result != nil || f.err != nil {
		return f.result, f.err
	}
	return roleList{Schema: "fitr.mcp.roles.v1", Roles: []roleSummary{}}, nil
}
func (f fixtureSource) review(ctx context.Context, _ string) (any, error) { return f.list(ctx) }
func (f fixtureSource) status(ctx context.Context, _ string) (any, error) { return f.list(ctx) }

func TestIndependentProtocolFixtures(t *testing.T) {
	data, err := os.ReadFile("testdata/protocol.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixtures []struct {
		Name              string
		Request, Response json.RawMessage
	}
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatal(err)
	}
	for _, fixture := range fixtures {
		t.Run(fixture.Name, func(t *testing.T) {
			var output bytes.Buffer
			if err := serve(t.Context(), io.NopCloser(bytes.NewReader(append(fixture.Request, '\n'))), &output, fixtureSource{}, "fixture"); err != nil {
				t.Fatal(err)
			}
			var got, want any
			if err := json.Unmarshal(output.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal(fixture.Response, &want); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("got %s\nwant %s", output.Bytes(), fixture.Response)
			}
		})
	}
}

func testRequest(method, extra string) string {
	if extra != "" {
		extra += ","
	}
	return `{"jsonrpc":"2.0","id":1,"method":"` + method + `","params":{` + extra + fixtureMeta + `}}`
}

func TestRejectAmbiguousAndMalformedWireInputs(t *testing.T) {
	tests := map[string]int{
		`{`: -32700, `[]`: -32600, `null`: -32600,
		`{"jsonrpc":"2.0","id":null,"method":"tools/list"}`:                           -32600,
		`{"jsonrpc":"2.0","id":1.5,"method":"tools/list"}`:                            -32600,
		`{"jsonrpc":"2.0","id":1,"id":2,"method":"tools/list"}`:                       -32600,
		`{"jsonrpc":"2.0","id":1,"method":"tools/list","params":[]}`:                  -32602,
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`:                  -32602,
		testRequest("initialize", ""):                                                 -32601,
		testRequest("resources/read", `"uri":"file:///secret"`):                       -32601,
		testRequest("tools/list", `"cursor":"unknown"`):                               -32602,
		testRequest("server/discover", `"extra":true`):                                -32602,
		testRequest("tools/call", `"name":"fitr_roles_list","arguments":null`):        -32602,
		testRequest("tools/call", `"name":"fitr_roles_list","requestState":"secret"`): -32602,
	}
	for input, code := range tests {
		t.Run(input, func(t *testing.T) {
			var output bytes.Buffer
			if err := serve(t.Context(), io.NopCloser(strings.NewReader(input+"\n")), &output, fixtureSource{}, "fixture"); err != nil {
				t.Fatal(err)
			}
			var reply response
			if err := json.Unmarshal(output.Bytes(), &reply); err != nil {
				t.Fatal(err)
			}
			if reply.Error == nil || reply.Error.Code != code {
				t.Fatalf("reply=%s want code %d", output.Bytes(), code)
			}
		})
	}
}

func TestMetadataIsPerRequestAndNeverAuthority(t *testing.T) {
	valid := testRequest("server/discover", "")
	missing := `{"jsonrpc":"2.0","id":2,"method":"tools/list"}`
	var output bytes.Buffer
	if err := serve(t.Context(), io.NopCloser(strings.NewReader(valid+"\n"+missing+"\n")), &output, fixtureSource{}, "fixture"); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(output.String()), "\n")
	if len(lines) != 2 || !strings.Contains(lines[0], `"supportedVersions":["2026-07-28"]`) || !strings.Contains(lines[1], `"code":-32602`) {
		t.Fatal(output.String())
	}
	for _, replacement := range []string{`null`, `[]`, `"secret"`} {
		input := strings.Replace(valid, `"io.modelcontextprotocol/clientCapabilities":{}`, `"io.modelcontextprotocol/clientCapabilities":`+replacement, 1)
		req, invalid := decodeRequest([]byte(input))
		if invalid != nil || validateMetadata(req) == nil {
			t.Fatalf("accepted invalid metadata %s", input)
		}
	}
}

func TestNotificationsProduceNoResponse(t *testing.T) {
	input := `{"jsonrpc":"2.0","method":"notifications/initialized"}` + "\n" + `{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":"unknown"}}` + "\n"
	var output bytes.Buffer
	if err := serve(t.Context(), io.NopCloser(strings.NewReader(input)), &output, fixtureSource{}, "fixture"); err != nil {
		t.Fatal(err)
	}
	if output.Len() != 0 {
		t.Fatal(output.String())
	}
}

func TestCatalogIsStableAndBoundToStrictSchemas(t *testing.T) {
	first, second := catalog(), catalog()
	if !reflect.DeepEqual(first, second) || len(first) != 3 {
		t.Fatal("unstable catalog")
	}
	for _, tool := range first {
		input, ok := tool["inputSchema"].(map[string]any)
		if !ok || input["type"] != "object" || input["additionalProperties"] != false {
			t.Fatalf("permissive input schema: %+v", tool)
		}
		output, ok := tool["outputSchema"].(map[string]any)
		if !ok || output["additionalProperties"] != false {
			t.Fatal("missing bounded output schema")
		}
		annotations, ok := tool["annotations"].(map[string]bool)
		if !ok || !annotations["readOnlyHint"] || annotations["openWorldHint"] {
			t.Fatal("incorrect tool annotations")
		}
	}
}

func TestRequestIDTypesRemainDistinctAndCanonical(t *testing.T) {
	tests := []struct {
		raw, key string
		valid    bool
	}{
		{`1`, "n:1", true}, {`"1"`, "s:1", true}, {`-0`, "n:0", true},
		{`"\u0061"`, "s:a", true}, {`"null"`, "s:null", true}, {`""`, "s:", true},
		{` null `, "", false}, {`true`, "", false}, {`1.5`, "", false},
		{`9223372036854775808`, "", false}, {`"` + strings.Repeat("a", 129) + `"`, "", false},
	}
	for _, test := range tests {
		key, valid := idKey([]byte(test.raw))
		if valid != test.valid || (valid && key != test.key) {
			t.Fatalf("id=%s key=%s valid=%t", test.raw, key, valid)
		}
	}
}

func TestRejectInvalidCapabilityShapesAndMetadataKeys(t *testing.T) {
	for _, value := range []string{`{"roots":null}`, `{"sampling":3}`, `{"elicitation":[]}`, `{"extensions":{"example/test":true}}`, `{"experimental":{"test":null}}`} {
		input := strings.Replace(testRequest("server/discover", ""), `"io.modelcontextprotocol/clientCapabilities":{}`, `"io.modelcontextprotocol/clientCapabilities":`+value, 1)
		req, invalid := decodeRequest([]byte(input))
		if invalid != nil || validateMetadata(req) == nil {
			t.Fatalf("accepted %s", input)
		}
	}
	for _, info := range []string{`null`, `{}`, `{"name":null,"version":"1"}`} {
		input := strings.Replace(testRequest("server/discover", ""), `"io.modelcontextprotocol/clientCapabilities":{}`, `"io.modelcontextprotocol/clientCapabilities":{},"io.modelcontextprotocol/clientInfo":`+info, 1)
		req, invalid := decodeRequest([]byte(input))
		if invalid != nil || validateMetadata(req) == nil {
			t.Fatalf("accepted %s", input)
		}
	}
}

func TestMalformedRequestRetainsUnambiguousID(t *testing.T) {
	for _, input := range []string{`{"jsonrpc":"2.0","id":"request-42","method":null}`, `{"jsonrpc":"2.0","id":"request-42","method":"tools/list","extra":true}`} {
		_, invalid := decodeRequest([]byte(input))
		if invalid == nil || string(invalid.ID) != `"request-42"` {
			t.Fatalf("lost readable ID: %+v", invalid)
		}
	}
	_, invalid := decodeRequest([]byte(`{"jsonrpc":"2.0","method":"notifications/cancelled","params":{"requestId":1},"extra":true}`))
	if invalid != nil {
		t.Fatal("malformed notification produced a response")
	}
}
