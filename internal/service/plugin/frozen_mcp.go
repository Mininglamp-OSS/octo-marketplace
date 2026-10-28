package plugin

import (
	"encoding/json"
	"net/url"
	"regexp"
	"strings"
)

var frozenMCPReference = regexp.MustCompile(`^\$\{[A-Za-z_][A-Za-z0-9_]*\}$`)

// Frozen MCP cannot carry concrete credentials. Use the migration boundary's
// conservative env/header rule: only empty values or whole ${NAME} references
// are durable. Transient non-pinned MCP validation remains unchanged.
// Validation never sanitizes or reconstructs an already frozen request.
func frozenMCPConfig(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	normalized, err := normalizeCapabilityMCP(string(raw))
	if err != nil {
		return nil, err
	}
	if len(normalized) == 0 {
		return nil, nil
	}
	var doc struct {
		Servers map[string]capabilityMCPServer `json:"mcpServers"`
	}
	if json.Unmarshal(normalized, &doc) != nil {
		return nil, invalidCapabilityMCP("invalid_json")
	}
	safe := func(value string) bool { return value == "" || frozenMCPReference.MatchString(value) }
	for _, server := range doc.Servers {
		for _, entries := range []map[string]string{server.Env, server.Headers} {
			for _, value := range entries {
				if !safe(value) {
					return nil, invalidCapabilityMCP("credential_not_persistable")
				}
			}
		}
		if server.URL != "" {
			u, err := url.Parse(server.URL)
			if err != nil {
				return nil, invalidCapabilityMCP("invalid_json")
			}
			if u.User != nil {
				return nil, invalidCapabilityMCP("credential_not_persistable")
			}
			query, err := url.ParseQuery(u.RawQuery)
			if err != nil {
				return nil, invalidCapabilityMCP("invalid_json")
			}
			for key, values := range query {
				if frozenMCPCredentialKey(key) {
					for _, value := range values {
						if !safe(value) {
							return nil, invalidCapabilityMCP("credential_not_persistable")
						}
					}
				}
			}
			// OAuth-style fragment parameters are credentials as well as query values.
			fragment, err := url.ParseQuery(u.Fragment)
			if err != nil {
				return nil, invalidCapabilityMCP("invalid_json")
			}
			for key, values := range fragment {
				if frozenMCPCredentialKey(key) {
					for _, value := range values {
						if !safe(value) {
							return nil, invalidCapabilityMCP("credential_not_persistable")
						}
					}
				}
			}
		}
		for i, arg := range server.Args {
			flag, value, hasValue := strings.Cut(arg, "=")
			if strings.HasPrefix(flag, "-") && frozenMCPCredentialKey(strings.TrimLeft(flag, "-")) {
				if !hasValue && i+1 < len(server.Args) {
					value = server.Args[i+1]
				}
				if !safe(value) {
					return nil, invalidCapabilityMCP("credential_not_persistable")
				}
			}
		}
	}
	return normalized, nil
}

func frozenMCPCredentialKey(key string) bool {
	key = strings.NewReplacer("-", "", "_", "", ".", "").Replace(strings.ToLower(key))
	for _, suffix := range []string{"token", "apikey", "authorization", "password", "passwd", "secret", "credential", "signature", "cookie"} {
		if strings.HasSuffix(key, suffix) {
			return true
		}
	}
	return key == "key" || key == "auth" || key == "sig" || key == "bearer"
}
