package model

import (
	"os"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestCapabilityInstallationOpenAPIMatchesStrictRequest(t *testing.T) {
	raw, err := os.ReadFile("../../docs/openapi/swagger.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var spec struct {
		Paths map[string]map[string]struct {
			OperationID string `yaml:"operationId"`
			RequestBody struct {
				Content map[string]struct {
					Schema struct {
						Ref   string `yaml:"$ref"`
						OneOf []any  `yaml:"oneOf"`
					} `yaml:"schema"`
				} `yaml:"content"`
			} `yaml:"requestBody"`
		} `yaml:"paths"`
		Components struct {
			Schemas map[string]struct {
				AdditionalProperties any      `yaml:"additionalProperties"`
				Required             []string `yaml:"required"`
			} `yaml:"schemas"`
		} `yaml:"components"`
	}
	if err := yaml.Unmarshal(raw, &spec); err != nil {
		t.Fatal(err)
	}
	operation := spec.Paths["/plugins/{plugin_id}/installations"]["post"]
	body := operation.RequestBody.Content["application/json"].Schema
	const schemaName = "internal_api_handler_plugin.createInstallationRequest"
	if operation.OperationID != "plugin.installation.create" || body.Ref != "#/components/schemas/"+schemaName || len(body.OneOf) != 0 {
		t.Fatal("installation request must reference its required schema directly")
	}
	schema := spec.Components.Schemas[schemaName]
	if schema.AdditionalProperties != false || len(schema.Required) != 1 || schema.Required[0] != "runtime_id" {
		t.Fatal("installation schema must require runtime_id and reject unknown fields")
	}
	if _, ok := spec.Paths["/plugins/install"]["post"]; !ok {
		t.Fatal("legacy route missing")
	}
}
