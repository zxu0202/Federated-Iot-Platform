package parameters

import (
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFileRejectsIncompleteAndUnknownDefinitions(t *testing.T) {
	path := filepath.Join(t.TempDir(), "constraints.json")
	if err := writeDocument(path, map[string]any{
		"contract_version": ContractVersion,
		"paths": map[string]any{
			"feature_state.nLag": map[string]any{"type": "integer", "nullable": false, "minimum": nil, "maximum": nil},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("constraint with an omitted allowed_values field was accepted")
	}

	if err := writeDocument(path, map[string]any{
		"contract_version": ContractVersion,
		"paths": map[string]any{
			"feature_state.nLag": map[string]any{"type": "integer", "nullable": false, "minimum": nil, "maximum": nil, "allowed_values": nil, "unexpected": true},
		},
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadFile(path); err == nil {
		t.Fatal("constraint with an unknown field was accepted")
	}
}

func TestMaterializeRequiresCompleteSharedAndAllowsSparseAgentOverrides(t *testing.T) {
	document := testDocument(t, map[string]Constraint{
		"feature_state.nLag": {Type: "integer"},
		"trend.threshold":    {Type: "number"},
	})
	referenceShared := map[string]any{
		"feature_state": map[string]any{"nLag": 8},
		"trend":         map[string]any{"threshold": 1.0},
	}
	referenceAgents := []any{
		map[string]any{"agent": 1, "segment": "EARLY", "parameters": map[string]any{}},
		map[string]any{"agent": 2, "segment": "MIDDLE", "parameters": map[string]any{}},
		map[string]any{"agent": 3, "segment": "LATE", "parameters": map[string]any{}},
	}
	_, err := document.Materialize(referenceShared, referenceAgents, map[string]any{"feature_state": map[string]any{"nLag": 8}}, []AgentOverride{
		{Agent: 1, Parameters: map[string]any{}}, {Agent: 2, Parameters: map[string]any{}}, {Agent: 3, Parameters: map[string]any{}},
	})
	assertValidationCode(t, err, "REQUEST_INVALID")

	materialized, err := document.Materialize(referenceShared, referenceAgents, map[string]any{
		"feature_state": map[string]any{"nLag": 9},
		"trend":         map[string]any{"threshold": 1.2},
	}, []AgentOverride{
		{Agent: 3, Parameters: map[string]any{"trend": map[string]any{"threshold": 2.5}}},
		{Agent: 1, Parameters: map[string]any{"feature_state": map[string]any{"nLag": 10}}},
		{Agent: 2, Parameters: map[string]any{}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if materialized.Shared["feature_state"].(map[string]any)["nLag"] != 9 {
		t.Fatalf("shared values were not materialized: %#v", materialized.Shared)
	}
	if materialized.Agents[0]["agent"] != 1 || materialized.Agents[0]["segment"] != "EARLY" {
		t.Fatalf("Agent overrides were not ordered by the frozen topology: %#v", materialized.Agents)
	}
	if got := materialized.Agents[0]["parameters"].(map[string]any)["feature_state"].(map[string]any)["nLag"]; got != 10 {
		t.Fatalf("Agent 1 sparse override = %#v, want nLag=10", materialized.Agents[0])
	}
	if len(materialized.Agents[1]["parameters"].(map[string]any)) != 0 {
		t.Fatalf("empty sparse override was expanded: %#v", materialized.Agents[1])
	}
}

func TestMaterializeRejectsUnknownTypeNonFiniteAndFixedTopologyValues(t *testing.T) {
	document := testDocument(t, map[string]Constraint{
		"feature_state.nLag": {Type: "integer"},
		"split.agent_count":  {Type: "integer", AllowedValues: []any{float64(3)}},
	})
	document.Paths["split.agent_count"] = Constraint{Type: "integer", Editable: false, AllowedValues: []any{float64(3)}}
	referenceShared := map[string]any{
		"feature_state": map[string]any{"nLag": 8},
		"split":         map[string]any{"agent_count": 3},
	}
	referenceAgents := []any{
		map[string]any{"agent": 1, "segment": "EARLY", "parameters": map[string]any{}},
		map[string]any{"agent": 2, "segment": "MIDDLE", "parameters": map[string]any{}},
		map[string]any{"agent": 3, "segment": "LATE", "parameters": map[string]any{}},
	}
	validAgents := []AgentOverride{{Agent: 1, Parameters: map[string]any{}}, {Agent: 2, Parameters: map[string]any{}}, {Agent: 3, Parameters: map[string]any{}}}
	for _, testCase := range []struct {
		name   string
		shared map[string]any
		code   string
	}{
		{name: "unknown path", shared: map[string]any{"feature_state": map[string]any{"nLag": 8, "unknown": 1}, "split": map[string]any{"agent_count": 3}}, code: "PARAMETER_NOT_ALLOWED"},
		{name: "wrong type", shared: map[string]any{"feature_state": map[string]any{"nLag": "8"}, "split": map[string]any{"agent_count": 3}}, code: "PARAMETER_OUT_OF_RANGE"},
		{name: "non finite", shared: map[string]any{"feature_state": map[string]any{"nLag": math.Inf(1)}, "split": map[string]any{"agent_count": 3}}, code: "PARAMETER_OUT_OF_RANGE"},
		{name: "fixed agent count", shared: map[string]any{"feature_state": map[string]any{"nLag": 8}, "split": map[string]any{"agent_count": 2}}, code: "PARAMETER_OUT_OF_RANGE"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := document.Materialize(referenceShared, referenceAgents, testCase.shared, validAgents)
			assertValidationCode(t, err, testCase.code)
		})
	}
	_, err := document.Materialize(referenceShared, referenceAgents, map[string]any{"feature_state": map[string]any{"nLag": 8}, "split": map[string]any{"agent_count": 3}}, []AgentOverride{
		{Agent: 1, Segment: "MIDDLE", Parameters: map[string]any{}}, {Agent: 2, Parameters: map[string]any{}}, {Agent: 3, Parameters: map[string]any{}},
	})
	assertValidationCode(t, err, "PARAMETER_NOT_ALLOWED")
	_, err = document.Materialize(referenceShared, referenceAgents, map[string]any{"feature_state": map[string]any{"nLag": 8}, "split": map[string]any{"agent_count": 3}}, []AgentOverride{
		{Agent: 1, Parameters: map[string]any{"split": map[string]any{"agent_count": 3}}}, {Agent: 2, Parameters: map[string]any{}}, {Agent: 3, Parameters: map[string]any{}},
	})
	assertValidationCode(t, err, "PARAMETER_NOT_ALLOWED")
}

func testDocument(t *testing.T, paths map[string]Constraint) *Document {
	t.Helper()
	for path, constraint := range paths {
		constraint.Nullable = false
		constraint.Editable = true
		paths[path] = constraint
	}
	return &Document{ContractVersion: ContractVersion, Paths: paths}
}

func writeDocument(path string, value any) error {
	encoded, err := json.Marshal(value)
	if err != nil {
		return err
	}
	return os.WriteFile(path, encoded, 0o600)
}

func assertValidationCode(t *testing.T, err error, want string) {
	t.Helper()
	validation, ok := err.(ValidationError)
	if !ok || validation.Code != want {
		t.Fatalf("validation error = %#v, want code %q", err, want)
	}
}
