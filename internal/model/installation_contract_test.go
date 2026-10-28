package model

import (
	"bytes"
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
	"gopkg.in/yaml.v3"
)

const (
	installationContractSpecPath = "../../docs/openapi/swagger.yaml"
	installationContractSpecURI  = "https://octo-marketplace.invalid/openapi/swagger.json"
	installationRoute            = "/plugins/{plugin_id}/installations"
	legacyInstallationRoute      = "/plugins/install"
)

type installationContract struct {
	document map[string]any
	compiler *jsonschema.Compiler
	resource *url.URL
}

func TestInstallationConsumerContract(t *testing.T) {
	contract := loadInstallationContract(t)

	newOperation := operationAt(t, contract, installationRoute, "post")
	if got := stringAt(t, newOperation["operationId"]); got != "plugin.installation.create" {
		t.Fatalf("POST %s operationId = %q, want %q", installationRoute, got, "plugin.installation.create")
	}
	newRequest := contract.compileAt(t, "paths", installationRoute, "post", "requestBody", "content", "application/json", "schema")
	newResponse := contract.compileAt(t, "paths", installationRoute, "post", "responses", "200", "content", "application/json", "schema")

	operationAt(t, contract, legacyInstallationRoute, "post")
	legacyRequest := contract.compileAt(t, "paths", legacyInstallationRoute, "post", "requestBody", "content", "application/json", "schema")
	legacyResponse := contract.compileAt(t, "paths", legacyInstallationRoute, "post", "responses", "200", "content", "application/json", "schema")

	t.Run("runtime requirement follows workspace connector mode", func(t *testing.T) {
		assertInvalidJSON(t, newRequest, `{}`)
		assertInvalidJSON(t, newRequest, `{"workspace_connectors":false}`)
		assertValidJSON(t, newRequest, `{"workspace_connectors":true}`)
		assertValidJSON(t, newRequest, `{"workspace_connectors":false,"runtime_id":"550e8400-e29b-41d4-a716-446655440000"}`)
	})

	t.Run("runtime id uses UUID format", func(t *testing.T) {
		assertInvalidJSON(t, newRequest, `{"workspace_connectors":false,"runtime_id":"runtime-one"}`)
	})

	t.Run("custom environment values are strings", func(t *testing.T) {
		assertValidJSON(t, newRequest, `{"workspace_connectors":true,"custom_env":{"MODE":"safe"}}`)
		assertInvalidJSON(t, newRequest, `{"workspace_connectors":true,"custom_env":{"MODE":1}}`)
		assertInvalidJSON(t, newRequest, `{"workspace_connectors":true,"custom_env":{"MODE":null}}`)
	})

	t.Run("resource name length counts Unicode characters", func(t *testing.T) {
		assertValidJSON(t, newRequest, `{"workspace_connectors":true,"resource_name":"`+strings.Repeat("名", 128)+`"}`)
		assertInvalidJSON(t, newRequest, `{"workspace_connectors":true,"resource_name":"`+strings.Repeat("名", 129)+`"}`)
	})

	t.Run("new request rejects unknown fields", func(t *testing.T) {
		assertInvalidJSON(t, newRequest, `{"workspace_connectors":true,"future_option":true}`)
	})

	t.Run("legacy route keeps opaque string identifiers and body fields", func(t *testing.T) {
		assertValidJSON(t, legacyRequest, `{"plugin_id":"plugin:legacy-id","workspace_id":"workspace/opaque","runtime_id":"runtime#opaque","custom_env":{"MODE":"legacy"}}`)
		assertValidJSON(t, legacyResponse, `{"data":{"agent_id":"agent:legacy"}}`)
		assertValidJSON(t, legacyResponse, `{"data":{"squad_id":"squad:legacy"}}`)
	})

	t.Run("connector result metadata is nullable strings", func(t *testing.T) {
		assertValidJSON(t, newResponse, `{"data":{"agent_id":"agent:opaque","connectors":[{"connector_id":"connector:opaque","created":true,"market_mcp_id":null,"name":"clock","version":null}]}}`)
		assertValidJSON(t, newResponse, `{"data":{"squad_id":"squad:opaque","connectors":[{"connector_id":"connector:team","created":true,"market_mcp_id":"market-mcp","name":"mail","version":"1.2.3"}]}}`)
		assertValidJSON(t, newResponse, `{"data":{"connectors":[{"connector_id":"connector:only","created":true,"market_mcp_id":null,"name":"drive","version":null}]}}`)
		assertInvalidJSON(t, newResponse, `{"data":{"connectors":[{"market_mcp_id":12}]}}`)
		assertInvalidJSON(t, newResponse, `{"data":{"connectors":[{"version":false}]}}`)
	})
}

func loadInstallationContract(t *testing.T) *installationContract {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(installationContractSpecPath))
	if err != nil {
		t.Fatal(err)
	}

	var document map[string]any
	if err := yaml.Unmarshal(raw, &document); err != nil {
		t.Fatalf("parse OpenAPI YAML: %v", err)
	}
	specJSON, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("encode OpenAPI document as JSON: %v", err)
	}
	schemaDocument, err := jsonschema.UnmarshalJSON(bytes.NewReader(specJSON))
	if err != nil {
		t.Fatalf("parse OpenAPI JSON document: %v", err)
	}

	compiler := jsonschema.NewCompiler()
	compiler.AssertFormat()
	resource, err := url.Parse(installationContractSpecURI)
	if err != nil {
		t.Fatal(err)
	}
	if err := compiler.AddResource(resource.String(), schemaDocument); err != nil {
		t.Fatalf("register OpenAPI document for reference resolution: %v", err)
	}
	return &installationContract{document: document, compiler: compiler, resource: resource}
}

func operationAt(t *testing.T, contract *installationContract, path, method string) map[string]any {
	t.Helper()
	paths := objectAt(t, contract.document["paths"])
	pathItem := objectAt(t, paths[path])
	return objectAt(t, pathItem[method])
}

func (contract *installationContract) compileAt(t *testing.T, pointer ...string) *jsonschema.Schema {
	t.Helper()
	tokens := make([]string, len(pointer))
	for i, token := range pointer {
		token = strings.ReplaceAll(token, "~", "~0")
		tokens[i] = strings.ReplaceAll(token, "/", "~1")
	}
	location := *contract.resource
	location.Fragment = "/" + strings.Join(tokens, "/")
	schema, err := contract.compiler.Compile(location.String())
	if err != nil {
		t.Fatalf("compile OpenAPI schema at %s: %v", location.String(), err)
	}
	return schema
}

func objectAt(t *testing.T, value any) map[string]any {
	t.Helper()
	object, ok := value.(map[string]any)
	if !ok {
		t.Fatalf("expected OpenAPI object, got %T", value)
	}
	return object
}

func stringAt(t *testing.T, value any) string {
	t.Helper()
	text, ok := value.(string)
	if !ok {
		t.Fatalf("expected OpenAPI string, got %T", value)
	}
	return text
}

func assertValidJSON(t *testing.T, schema *jsonschema.Schema, raw string) {
	t.Helper()
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse test JSON %s: %v", raw, err)
	}
	if err := schema.Validate(instance); err != nil {
		t.Errorf("expected consumer JSON to satisfy schema, got %v: %s", err, raw)
	}
}

func assertInvalidJSON(t *testing.T, schema *jsonschema.Schema, raw string) {
	t.Helper()
	instance, err := jsonschema.UnmarshalJSON(strings.NewReader(raw))
	if err != nil {
		t.Fatalf("parse test JSON %s: %v", raw, err)
	}
	if err := schema.Validate(instance); err == nil {
		t.Errorf("expected consumer JSON to violate schema: %s", raw)
	}
}
