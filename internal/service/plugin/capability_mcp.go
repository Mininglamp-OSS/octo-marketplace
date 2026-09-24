package plugin

import (
	"bytes"
	"encoding/json"
	"io"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/Mininglamp-OSS/octo-marketplace/internal/model"
)

var capabilityMCPServerName = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
var capabilityMCPHeaderName = regexp.MustCompile("^[!#$%&'*+.^_`|~0-9A-Za-z-]+$")

type capabilityMCPServer struct {
	Type    string            `json:"type,omitempty"`
	Command string            `json:"command,omitempty"`
	Args    []string          `json:"args,omitempty"`
	Env     map[string]string `json:"env,omitempty"`
	URL     string            `json:"url,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
}

// normalizeCapabilityMCP follows Fleet test 49a8266's portable MCP contract.
// Empty legacy defaults mean no MCP; every non-empty configuration is validated
// without dropping unknown fields or changing environment/header/argument values.
func normalizeCapabilityMCP(raw string) (json.RawMessage, error) {
	if !utf8.ValidString(raw) {
		return nil, invalidCapabilityMCP("invalid_json")
	}
	object, err := capabilityMCPObject(json.RawMessage(raw))
	if err != nil {
		return nil, err
	}
	if len(object) == 0 {
		return nil, nil
	}
	if len(object) != 1 || object["mcpServers"] == nil {
		return nil, invalidCapabilityMCP("unknown_field")
	}
	servers, err := capabilityMCPObject(object["mcpServers"])
	if err != nil {
		return nil, err
	}
	if len(servers) == 0 {
		return nil, nil
	}
	if len(servers) > 100 {
		return nil, ErrTooLarge
	}
	out := make(map[string]capabilityMCPServer, len(servers))
	for name, rawServer := range servers {
		if !capabilityMCPServerName.MatchString(name) {
			return nil, invalidCapabilityMCP("invalid_server_name")
		}
		server, err := normalizeCapabilityMCPServer(rawServer)
		if err != nil {
			return nil, err
		}
		out[name] = server
	}
	encoded, err := json.Marshal(struct {
		Servers map[string]capabilityMCPServer `json:"mcpServers"`
	}{Servers: out})
	if err != nil {
		return nil, invalidCapabilityMCP("invalid_json")
	}
	if len(encoded) > model.MaxMCPConfigBytes {
		return nil, ErrTooLarge
	}
	return encoded, nil
}

func normalizeCapabilityMCPServer(raw json.RawMessage) (capabilityMCPServer, error) {
	var out capabilityMCPServer
	object, err := capabilityMCPObject(raw)
	if err != nil {
		return out, err
	}
	for key := range object {
		switch key {
		case "type", "command", "args", "env", "url", "headers":
		default:
			return out, invalidCapabilityMCP("unknown_field")
		}
	}
	for key, target := range map[string]*string{"type": &out.Type, "command": &out.Command, "url": &out.URL} {
		if value, present := object[key]; present {
			if *target, err = capabilityMCPString(value); err != nil {
				return out, err
			}
		}
	}
	if value, present := object["args"]; present {
		var items []json.RawMessage
		if json.Unmarshal(value, &items) != nil || items == nil {
			return out, invalidCapabilityMCP("invalid_args")
		}
		if len(items) > 100 {
			return out, ErrTooLarge
		}
		for _, item := range items {
			argument, err := capabilityMCPString(item)
			if err != nil || strings.ContainsRune(argument, 0) {
				return out, invalidCapabilityMCP("invalid_args")
			}
			out.Args = append(out.Args, argument)
		}
	}
	if out.Env, err = capabilityMCPStringMap(object["env"], false); err != nil {
		return out, err
	}
	if out.Headers, err = capabilityMCPStringMap(object["headers"], true); err != nil {
		return out, err
	}
	out.Type = strings.ToLower(strings.TrimSpace(out.Type))
	out.Command = strings.TrimSpace(out.Command)
	out.URL = strings.TrimSpace(out.URL)
	if (out.Command != "") == (out.URL != "") {
		return out, invalidCapabilityMCP("invalid_transport")
	}
	if out.Command != "" {
		if strings.ContainsRune(out.Command, 0) || out.Type != "" || len(out.Headers) > 0 {
			return out, invalidCapabilityMCP("invalid_stdio")
		}
	} else {
		if out.Type == "" {
			out.Type = "http"
		}
		parsed, err := url.Parse(out.URL)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "http" && parsed.Scheme != "https") ||
			(out.Type != "http" && out.Type != "sse" && out.Type != "streamable-http") || len(out.Args) > 0 || len(out.Env) > 0 {
			return out, invalidCapabilityMCP("invalid_remote")
		}
	}
	return out, nil
}

func capabilityMCPStringMap(raw json.RawMessage, headers bool) (map[string]string, error) {
	if raw == nil {
		return nil, nil
	}
	object, err := capabilityMCPObject(raw)
	if err != nil {
		return nil, err
	}
	if len(object) > 100 {
		return nil, ErrTooLarge
	}
	out := make(map[string]string, len(object))
	for key, rawValue := range object {
		value, err := capabilityMCPString(rawValue)
		if err != nil || strings.ContainsRune(value, 0) {
			return nil, invalidCapabilityMCP("invalid_values")
		}
		if headers {
			if !capabilityMCPHeaderName.MatchString(key) || strings.ContainsAny(value, "\r\n") {
				return nil, invalidCapabilityMCP("invalid_headers")
			}
		} else {
			if !envName.MatchString(key) {
				return nil, invalidCapabilityMCP("invalid_env")
			}
			if len(value) > 1<<20 {
				return nil, ErrTooLarge
			}
		}
		out[key] = value
	}
	return out, nil
}

func capabilityMCPString(raw json.RawMessage) (string, error) {
	var value *string
	if json.Unmarshal(raw, &value) != nil || value == nil || !utf8.Valid(raw) {
		return "", invalidCapabilityMCP("invalid_value_type")
	}
	// JSON decoding replaces unpaired UTF-16 surrogates with U+FFFD. Validate
	// original escapes so credentials cannot silently change during decoding.
	// Syntax is already valid, hence each Unicode escape has four hex digits.
	for i := 0; i < len(raw); i++ {
		if raw[i] != '\\' {
			continue
		}
		i++
		if raw[i] != 'u' {
			continue
		}
		r, _ := strconv.ParseUint(string(raw[i+1:i+5]), 16, 16)
		i += 4
		switch {
		case r >= 0xd800 && r <= 0xdbff:
			if i+6 >= len(raw) || raw[i+1] != '\\' || raw[i+2] != 'u' {
				return "", invalidCapabilityMCP("invalid_unicode")
			}
			low, _ := strconv.ParseUint(string(raw[i+3:i+7]), 16, 16)
			if low < 0xdc00 || low > 0xdfff {
				return "", invalidCapabilityMCP("invalid_unicode")
			}
			i += 6
		case r >= 0xdc00 && r <= 0xdfff:
			return "", invalidCapabilityMCP("invalid_unicode")
		}
	}
	return *value, nil
}

// Decode each object explicitly so duplicate properties cannot overwrite a
// server or secret silently. Raw values retain their type for null rejection.
func capabilityMCPObject(raw json.RawMessage) (map[string]json.RawMessage, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	start, err := decoder.Token()
	if err != nil || start != json.Delim('{') {
		return nil, invalidCapabilityMCP("invalid_object")
	}
	out := map[string]json.RawMessage{}
	for decoder.More() {
		token, err := decoder.Token()
		key, ok := token.(string)
		if err != nil || !ok {
			return nil, invalidCapabilityMCP("invalid_object")
		}
		if _, exists := out[key]; exists {
			return nil, invalidCapabilityMCP("duplicate_field")
		}
		var value json.RawMessage
		if decoder.Decode(&value) != nil {
			return nil, invalidCapabilityMCP("invalid_json")
		}
		out[key] = value
	}
	if end, err := decoder.Token(); err != nil || end != json.Delim('}') {
		return nil, invalidCapabilityMCP("invalid_object")
	}
	if decoder.Decode(new(json.RawMessage)) != io.EOF {
		return nil, invalidCapabilityMCP("invalid_json")
	}
	return out, nil
}

func invalidCapabilityMCP(reason string) error {
	return installationInvalid("definition.experts.mcp_config", reason)
}
