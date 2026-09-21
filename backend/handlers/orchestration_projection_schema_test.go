// PAIMOS — Copyright (C) 2026 Markus Barta
package handlers_test

import (
	"encoding/json"
	"os"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/inspr-at/paimos/backend/dispatchprofile"
	"github.com/inspr-at/paimos/backend/handlers"
)

func TestOrchestrationSchemaClosedAndFleetV2Unchanged(t *testing.T) {
	raw, err := os.ReadFile("../contracts/orchestration-v1.schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema map[string]any
	if err = json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	definitions := schema["$defs"].(map[string]any)
	apiRaw, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var api map[string]any
	if err = json.Unmarshal(apiRaw, &api); err != nil {
		t.Fatal(err)
	}
	components := api["components"].(map[string]any)["schemas"].(map[string]any)
	var check func(any)
	check = func(value any) {
		switch v := value.(type) {
		case map[string]any:
			if ref, ok := v["$ref"].(string); ok {
				if !strings.HasPrefix(ref, "#/$defs/") || definitions[strings.TrimPrefix(ref, "#/$defs/")] == nil {
					t.Fatal("unresolved schema reference")
				}
			}
			if v["type"] == "object" && v["additionalProperties"] != false {
				t.Fatal("open privacy boundary")
			}
			for _, child := range v {
				check(child)
			}
		case []any:
			for _, child := range v {
				check(child)
			}
		}
	}
	check(schema)
	for name, definition := range definitions {
		wire, _ := json.Marshal(definition)
		var openapiDefinition any
		if err := json.Unmarshal([]byte(strings.ReplaceAll(string(wire), "#/$defs/", "#/components/schemas/")), &openapiDefinition); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(openapiDefinition, components[name]) {
			t.Fatalf("standalone/OpenAPI contract mismatch: %s", name)
		}
	}
	dispatch := definitions["HarnessDispatchProfile"].(map[string]any)["properties"].(map[string]any)
	if len(dispatch) != 8 || dispatch["family"] != nil || dispatch["tier"] != nil {
		t.Fatal("model registry metadata changed the fleet dispatch contract")
	}
	modelPattern, err := regexp.Compile(dispatch["model"].(map[string]any)["pattern"].(string))
	if err != nil {
		t.Fatal(err)
	}
	// Current catalog fixtures include Pi provider/id models; historical
	// snapshots below must remain valid without rewriting their pinned values.
	for _, profile := range dispatchprofile.List() {
		if !modelPattern.MatchString(profile.Model) {
			t.Fatalf("fleet schema rejects current model %q", profile.Model)
		}
	}
	root := definitions["OrchestrationSnapshotV1"].(map[string]any)["properties"].(map[string]any)
	if root["schema_version"].(map[string]any)["const"] != float64(1) || root["fleet"].(map[string]any)["$ref"] != "#/$defs/WorkerFleetSnapshotV2" || root["project_coordination"].(map[string]any)["maxItems"] != float64(100) {
		t.Fatal("version or bound drift")
	}
	fixture, err := os.ReadFile("../contracts/fixtures/orchestration-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(fixture)))
	decoder.DisallowUnknownFields()
	var snapshot handlers.OrchestrationSnapshotV1
	if err := decoder.Decode(&snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.SchemaVersion != 1 || snapshot.Fleet.SchemaVersion != 2 || snapshot.Fleet.Workers == nil {
		t.Fatal("fixture schema mismatch")
	}
	for _, worker := range snapshot.Fleet.Workers {
		if profile := worker.DispatchProfile; profile != nil && !modelPattern.MatchString(profile.Model) {
			t.Fatalf("fleet schema rejects historical model %q", profile.Model)
		}
	}
	for _, path := range []string{"/api/agent-mode/orchestration/v1", "/api/agent-mode/projects/{projectID}/orchestration/v1"} {
		operation := api["paths"].(map[string]any)[path].(map[string]any)["get"].(map[string]any)
		ref := operation["responses"].(map[string]any)["200"].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)["$ref"]
		if ref != "#/components/schemas/OrchestrationSnapshotV1" {
			t.Fatal("versioned route schema drift")
		}
	}
}
