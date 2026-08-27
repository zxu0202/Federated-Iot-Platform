package domain

import (
	"bytes"
	"testing"
)

func TestCanonicalJSONIsStableAcrossMapOrder(t *testing.T) {
	left, err := CanonicalJSON(map[string]any{"b": 2, "a": map[string]any{"z": true, "x": "value"}})
	if err != nil {
		t.Fatal(err)
	}
	right, err := CanonicalJSON(map[string]any{"a": map[string]any{"x": "value", "z": true}, "b": 2})
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(left, right) || SHA256Hex(left) != SHA256Hex(right) {
		t.Fatalf("canonical JSON is not stable: %s != %s", left, right)
	}
}

func TestValidateAgentSetRequiresExactlyFrozenAgents(t *testing.T) {
	valid := []AgentOverride{{Agent: 3, Parameters: map[string]any{}}, {Agent: 1, Parameters: map[string]any{}}, {Agent: 2, Parameters: map[string]any{}}}
	if err := ValidateAgentSet(valid); err != nil {
		t.Fatalf("expected valid set: %v", err)
	}
	if err := ValidateAgentSet([]AgentOverride{{Agent: 1, Parameters: map[string]any{}}, {Agent: 1, Parameters: map[string]any{}}, {Agent: 2, Parameters: map[string]any{}}}); err == nil {
		t.Fatal("duplicate Agent 1 was accepted")
	}
	if err := ValidateAgentSet([]AgentOverride{{Agent: 1, Parameters: map[string]any{"nLag": 8}}, {Agent: 2, Parameters: map[string]any{}}, {Agent: 3, Parameters: map[string]any{}}}); err == nil {
		t.Fatal("locked Agent parameter was accepted")
	}
}
