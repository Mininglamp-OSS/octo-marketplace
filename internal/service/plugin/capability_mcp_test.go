package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

func TestNormalizeCapabilityMCPEmptyDefaults(t *testing.T) {
	for _, raw := range []string{`{}`, ` {"mcpServers":{}} `} {
		out, err := normalizeCapabilityMCP(raw)
		if err != nil || out != nil {
			t.Fatalf("out=%s err=%v", out, err)
		}
	}
}

func TestNormalizeCapabilityMCPTransportsPreserveValues(t *testing.T) {
	const raw = `{"mcpServers":{
		"stdio":{"command":"  launch  ","args":["", " --secret=abc ", "line\nnext"],"env":{"TOKEN":"  abc\nxyz\t ","EMPTY":""}},
		"remote":{"url":"  https://example.test/mcp  ","headers":{"Authorization":" Bearer abc ","X-Empty":""}},
		"events":{"type":" SSE ","url":"https://example.test/events"},
		"stream":{"type":" STREAMABLE-HTTP ","url":"http://example.test/mcp"}
	}}`
	out, err := normalizeCapabilityMCP(raw)
	if err != nil {
		t.Fatal(err)
	}
	var decoded struct {
		Servers map[string]capabilityMCPServer `json:"mcpServers"`
	}
	if err := json.Unmarshal(out, &decoded); err != nil {
		t.Fatal(err)
	}
	stdio := decoded.Servers["stdio"]
	if stdio.Command != "launch" || stdio.Type != "" || !reflect.DeepEqual(stdio.Args, []string{"", " --secret=abc ", "line\nnext"}) ||
		!reflect.DeepEqual(stdio.Env, map[string]string{"TOKEN": "  abc\nxyz\t ", "EMPTY": ""}) {
		t.Fatal("stdio normalization changed a process value")
	}
	remote := decoded.Servers["remote"]
	if remote.URL != "https://example.test/mcp" || remote.Type != "http" || !reflect.DeepEqual(remote.Headers, map[string]string{"Authorization": " Bearer abc ", "X-Empty": ""}) {
		t.Fatal("remote normalization changed a header value")
	}
	if decoded.Servers["events"].Type != "sse" || decoded.Servers["stream"].Type != "streamable-http" {
		t.Fatal("remote transport type was not normalized")
	}
	again, err := normalizeCapabilityMCP(string(out))
	if err != nil || string(again) != string(out) {
		t.Fatal("normalization is not byte-stable")
	}
}

func TestNormalizeCapabilityMCPRejectsInvalidConfiguration(t *testing.T) {
	for _, tc := range []struct{ name, raw string }{
		{"null_root", `null`},
		{"null_servers", `{"mcpServers":null}`},
		{"null_server", `{"mcpServers":{"a":null}}`},
		{"null_command", `{"mcpServers":{"a":{"command":null}}}`},
		{"null_args", `{"mcpServers":{"a":{"command":"x","args":null}}}`},
		{"null_argument", `{"mcpServers":{"a":{"command":"x","args":[null]}}}`},
		{"null_env", `{"mcpServers":{"a":{"command":"x","env":null}}}`},
		{"null_env_value", `{"mcpServers":{"a":{"command":"x","env":{"TOKEN":null}}}}`},
		{"number_env_value", `{"mcpServers":{"a":{"command":"x","env":{"TOKEN":1}}}}`},
		{"null_header_value", `{"mcpServers":{"a":{"url":"https://example.test","headers":{"Token":null}}}}`},
		{"unknown_root", `{"mcpServers":{},"other":true}`},
		{"unknown_server", `{"mcpServers":{"a":{"command":"x","disabled":true}}}`},
		{"duplicate_root", `{"mcpServers":{"a":{"command":"x"}},"mcpServers":{}}`},
		{"duplicate_server", `{"mcpServers":{"a":{"command":"x"},"a":{"command":"y"}}}`},
		{"duplicate_field", `{"mcpServers":{"a":{"command":"x","command":"y"}}}`},
		{"duplicate_env", `{"mcpServers":{"a":{"command":"x","env":{"TOKEN":"x","TOKEN":"y"}}}}`},
		{"trailing_json", `{"mcpServers":{}} {}`},
		{"server_name", `{"mcpServers":{"with.dot":{"command":"x"}}}`},
		{"neither_transport", `{"mcpServers":{"a":{}}}`},
		{"both_transports", `{"mcpServers":{"a":{"command":"x","url":"https://example.test"}}}`},
		{"stdio_type", `{"mcpServers":{"a":{"command":"x","type":"stdio"}}}`},
		{"stdio_headers", `{"mcpServers":{"a":{"command":"x","headers":{"Token":"x"}}}}`},
		{"remote_args", `{"mcpServers":{"a":{"url":"https://example.test","args":["x"]}}}`},
		{"remote_env", `{"mcpServers":{"a":{"url":"https://example.test","env":{"TOKEN":"x"}}}}`},
		{"remote_scheme", `{"mcpServers":{"a":{"url":"ftp://example.test"}}}`},
		{"remote_relative", `{"mcpServers":{"a":{"url":"/mcp"}}}`},
		{"remote_type", `{"mcpServers":{"a":{"url":"https://example.test","type":"websocket"}}}`},
		{"command_nul", `{"mcpServers":{"a":{"command":"x\u0000"}}}`},
		{"argument_nul", `{"mcpServers":{"a":{"command":"x","args":["x\u0000"]}}}`},
		{"argument_surrogate", `{"mcpServers":{"a":{"command":"x","args":["\ud800"]}}}`},
		{"env_nul", `{"mcpServers":{"a":{"command":"x","env":{"TOKEN":"x\u0000"}}}}`},
		{"env_surrogate", `{"mcpServers":{"a":{"command":"x","env":{"TOKEN":"\udfff"}}}}`},
		{"env_name", `{"mcpServers":{"a":{"command":"x","env":{"not-valid":"x"}}}}`},
		{"header_name", `{"mcpServers":{"a":{"url":"https://example.test","headers":{"not valid":"x"}}}}`},
		{"header_crlf", `{"mcpServers":{"a":{"url":"https://example.test","headers":{"Token":"x\r\ny"}}}}`},
		{"header_nul", `{"mcpServers":{"a":{"url":"https://example.test","headers":{"Token":"x\u0000"}}}}`},
		{"header_surrogate", `{"mcpServers":{"a":{"url":"https://example.test","headers":{"Token":"\ud800\ud801"}}}}`},
		{"invalid_utf8", "{\"mcpServers\":{\"a\":{\"command\":\"\xff\"}}}"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := normalizeCapabilityMCP(tc.raw)
			var invalid *InstallationValidationError
			if !errors.As(err, &invalid) || invalid.Field != "definition.experts.mcp_config" {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestCapabilityMCPStringPreservesValidUnicode(t *testing.T) {
	for _, tc := range []struct{ raw, want string }{
		{`"\ud83d\ude00"`, "😀"},
		{`"\uFFFD"`, "�"},
		{`"�"`, "�"},
		{`"\\ud800"`, `\ud800`},
		{`"prefix\\\ud83d\ude00suffix"`, "prefix\\😀suffix"},
	} {
		value, err := capabilityMCPString(json.RawMessage(tc.raw))
		if err != nil || value != tc.want {
			t.Fatalf("valid Unicode changed: raw=%s err=%v", tc.raw, err)
		}
	}
}

func TestNormalizeCapabilityMCPLimits(t *testing.T) {
	for _, count := range []int{100, 101} {
		servers, values, args := map[string]any{}, map[string]string{}, make([]string, count)
		for i := range count {
			servers[fmt.Sprintf("s%d", i)] = map[string]any{"command": "x"}
			values[fmt.Sprintf("K%d", i)] = "x"
		}
		for name, configuration := range map[string]any{
			"servers": map[string]any{"mcpServers": servers},
			"args":    map[string]any{"mcpServers": map[string]any{"a": map[string]any{"command": "x", "args": args}}},
			"env":     map[string]any{"mcpServers": map[string]any{"a": map[string]any{"command": "x", "env": values}}},
			"headers": map[string]any{"mcpServers": map[string]any{"a": map[string]any{"url": "https://example.test", "headers": values}}},
		} {
			t.Run(fmt.Sprintf("%s_%d", name, count), func(t *testing.T) {
				raw, err := json.Marshal(configuration)
				if err != nil {
					t.Fatal(err)
				}
				_, err = normalizeCapabilityMCP(string(raw))
				if count == 100 && err != nil || count == 101 && !errors.Is(err, ErrTooLarge) {
					t.Fatalf("err=%v", err)
				}
			})
		}
	}
	const base = `{"mcpServers":{"a":{"command":"x","args":[""]}}}`
	for _, extra := range []int{0, 1} {
		raw := strings.Replace(base, `[""]`, `["`+strings.Repeat("x", model.MaxMCPConfigBytes-len(base)+extra)+`"]`, 1)
		out, err := normalizeCapabilityMCP(raw)
		if extra == 0 && (err != nil || len(out) != model.MaxMCPConfigBytes) || extra == 1 && !errors.Is(err, ErrTooLarge) {
			t.Fatalf("normalized-byte boundary: bytes=%d err=%v", len(out), err)
		}
	}
	if _, err := normalizeCapabilityMCP(strings.Repeat(" ", model.MaxMCPConfigBytes) + base); err != nil {
		t.Fatalf("raw whitespace must not consume normalized budget: %v", err)
	}
	// JSON escaping can exceed the normalized limit even when the raw input
	// is well below it; check the final encoded bytes, not just source length.
	escaped := strings.Replace(base, `[""]`, `["`+strings.Repeat("<", model.MaxMCPConfigBytes/6)+`"]`, 1)
	if _, err := normalizeCapabilityMCP(escaped); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("escaped output exceeded the normalized budget: %v", err)
	}
}

func TestNormalizeCapabilityMCPErrorsDoNotExposeValues(t *testing.T) {
	_, err := normalizeCapabilityMCP(`{"mcpServers":{"secret-server":{"url":"https://example.test","headers":{"Authorization":"Bearer secret-value\n"}}}}`)
	var invalid *InstallationValidationError
	if !errors.As(err, &invalid) {
		t.Fatal("expected validation error")
	}
	if strings.Contains(err.Error()+invalid.Field+invalid.Reason, "secret") {
		t.Fatal("error exposed submitted values")
	}
}
