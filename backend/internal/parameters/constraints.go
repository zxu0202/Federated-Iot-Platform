// Package parameters validates versioned CUSTOM parameter profiles.
package parameters

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"sort"
	"strings"
)

const ContractVersion = "parameter-constraints.v1"

// Constraint is the complete validation rule for one editable shared leaf.
// Nil bounds and allowed values mean that the corresponding restriction is
// deliberately not applied.
type Constraint struct {
	Type          string
	Editable      bool
	Nullable      bool
	Minimum       *float64
	Maximum       *float64
	AllowedValues []any
}

// Document is the versioned, deployment-supplied allowlist for CUSTOM values.
type Document struct {
	ContractVersion string
	Paths           map[string]Constraint
}

// AgentOverride is a sparse parameter override for one generic S1 Agent.
// Segment is accepted only for import and must match the reference profile.
type AgentOverride struct {
	Agent      int
	Segment    string
	Parameters map[string]any
}

// MaterializedProfile contains a complete shared configuration and the three
// ordered, sparse Agent override objects stored in an immutable CUSTOM profile.
type MaterializedProfile struct {
	Shared map[string]any
	Agents []map[string]any
}

// ValidationError is translated by the storage boundary into a stable API
// error without leaking deployment paths or parser details.
type ValidationError struct {
	Code    string
	Field   string
	Message string
}

func (e ValidationError) Error() string { return e.Message }

// LoadFile loads only an explicit local constraints document. There is no
// built-in fallback because an absent or incomplete allowlist must fail safely.
func LoadFile(path string) (*Document, error) {
	if strings.TrimSpace(path) == "" {
		return nil, fmt.Errorf("parameter constraints file is not configured")
	}
	contents, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("parameter constraints file cannot be read")
	}
	var raw struct {
		ContractVersion string                     `json:"contract_version"`
		Paths           map[string]json.RawMessage `json:"paths"`
	}
	if err := json.Unmarshal(contents, &raw); err != nil || raw.ContractVersion == "" || len(raw.Paths) == 0 {
		return nil, fmt.Errorf("parameter constraints file is invalid")
	}
	document := &Document{ContractVersion: raw.ContractVersion, Paths: make(map[string]Constraint, len(raw.Paths))}
	for path, encoded := range raw.Paths {
		constraint, err := decodeConstraint(encoded)
		if err != nil {
			return nil, fmt.Errorf("parameter constraints file is invalid")
		}
		document.Paths[path] = constraint
	}
	return document, nil
}

func decodeConstraint(encoded json.RawMessage) (Constraint, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &object); err != nil {
		return Constraint{}, err
	}
	for _, key := range []string{"type", "editable", "nullable", "minimum", "maximum", "allowed_values"} {
		if _, ok := object[key]; !ok {
			return Constraint{}, fmt.Errorf("constraint %s is missing", key)
		}
	}
	if len(object) != 6 {
		return Constraint{}, fmt.Errorf("constraint contains an unknown field")
	}
	var constraint Constraint
	if err := json.Unmarshal(object["type"], &constraint.Type); err != nil {
		return Constraint{}, err
	}
	if err := json.Unmarshal(object["editable"], &constraint.Editable); err != nil {
		return Constraint{}, err
	}
	if err := json.Unmarshal(object["nullable"], &constraint.Nullable); err != nil {
		return Constraint{}, err
	}
	if string(object["minimum"]) != "null" {
		var minimum float64
		if err := json.Unmarshal(object["minimum"], &minimum); err != nil {
			return Constraint{}, err
		}
		constraint.Minimum = &minimum
	}
	if string(object["maximum"]) != "null" {
		var maximum float64
		if err := json.Unmarshal(object["maximum"], &maximum); err != nil {
			return Constraint{}, err
		}
		constraint.Maximum = &maximum
	}
	if string(object["allowed_values"]) != "null" {
		if err := json.Unmarshal(object["allowed_values"], &constraint.AllowedValues); err != nil || constraint.AllowedValues == nil {
			return Constraint{}, fmt.Errorf("allowed_values must be an array or null")
		}
	}
	if err := constraint.validateDefinition(); err != nil {
		return Constraint{}, err
	}
	return constraint, nil
}

// ValidateReference proves that this document is a complete allowlist for the
// currently seeded immutable REFERENCE shared parameter shape.
func (d *Document) ValidateReference(reference map[string]any) error {
	if d == nil || d.ContractVersion != ContractVersion || len(d.Paths) == 0 {
		return fmt.Errorf("parameter constraints document is missing or invalid")
	}
	leaves := make(map[string]any)
	if err := flattenReference("", reference, leaves); err != nil {
		return err
	}
	if len(leaves) != len(d.Paths) {
		return fmt.Errorf("parameter constraints document is incomplete")
	}
	for path, value := range leaves {
		constraint, ok := d.Paths[path]
		if !ok {
			return fmt.Errorf("parameter constraints document is incomplete")
		}
		if err := constraint.validateValue(value); err != nil {
			return fmt.Errorf("parameter constraints document rejects reference value")
		}
	}
	for path := range d.Paths {
		if _, ok := leaves[path]; !ok {
			return fmt.Errorf("parameter constraints document has an unknown path")
		}
	}
	fixedPaths := map[string]bool{
		"split.agent_count":              true,
		"global_surrogate.leave_one_out": true,
	}
	for path, constraint := range d.Paths {
		if constraint.Editable == fixedPaths[path] {
			return fmt.Errorf("parameter constraints document has an invalid editable path set")
		}
	}
	return nil
}

// Materialize validates a complete CUSTOM shared configuration plus three
// sparse Agent overrides against the reference shape and returns a canonical
// topology-preserving representation.
func (d *Document) Materialize(referenceShared map[string]any, referenceAgents []any, shared map[string]any, agents []AgentOverride) (MaterializedProfile, error) {
	if err := d.ValidateReference(referenceShared); err != nil {
		return MaterializedProfile{}, err
	}
	if shared == nil {
		return MaterializedProfile{}, ValidationError{Code: "REQUEST_INVALID", Field: "shared_parameters", Message: "shared_parameters must contain every editable reference parameter."}
	}
	completed, err := d.validateCompleteObject("shared_parameters", referenceShared, shared)
	if err != nil {
		return MaterializedProfile{}, err
	}
	expectedAgents, err := referenceAgentSegments(referenceAgents)
	if err != nil {
		return MaterializedProfile{}, err
	}
	if len(agents) != len(expectedAgents) {
		return MaterializedProfile{}, ValidationError{Code: "REQUEST_INVALID", Field: "agents", Message: "agents must contain exactly Agent 1, Agent 2, and Agent 3."}
	}
	seen := make(map[int]bool, len(expectedAgents))
	materializedAgents := make([]map[string]any, 0, len(expectedAgents))
	for _, agent := range agents {
		segment, ok := expectedAgents[agent.Agent]
		if !ok || seen[agent.Agent] {
			return MaterializedProfile{}, ValidationError{Code: "REQUEST_INVALID", Field: "agents", Message: "agents must contain each of 1, 2, and 3 exactly once."}
		}
		seen[agent.Agent] = true
		if agent.Segment != "" && agent.Segment != segment {
			return MaterializedProfile{}, ValidationError{Code: "PARAMETER_NOT_ALLOWED", Field: fmt.Sprintf("agents[%d].segment", agent.Agent), Message: "Agent segment is fixed by the S1 topology."}
		}
		if agent.Parameters == nil {
			return MaterializedProfile{}, ValidationError{Code: "REQUEST_INVALID", Field: fmt.Sprintf("agents[%d].parameters", agent.Agent), Message: "Agent parameters must be an object."}
		}
		override, err := d.validateSparseObject(fmt.Sprintf("agents[%d].parameters", agent.Agent), referenceShared, agent.Parameters)
		if err != nil {
			return MaterializedProfile{}, err
		}
		materializedAgents = append(materializedAgents, map[string]any{"agent": agent.Agent, "segment": segment, "parameters": override})
	}
	sort.Slice(materializedAgents, func(left, right int) bool {
		return materializedAgents[left]["agent"].(int) < materializedAgents[right]["agent"].(int)
	})
	return MaterializedProfile{Shared: completed, Agents: materializedAgents}, nil
}

// APIResponse returns only declarative validation metadata, never the local
// file path or parser diagnostics.
func (d *Document) APIResponse() map[string]any {
	paths := make(map[string]any, len(d.Paths))
	for path, constraint := range d.Paths {
		paths[path] = map[string]any{
			"type":           constraint.Type,
			"editable":       constraint.Editable,
			"nullable":       constraint.Nullable,
			"minimum":        constraint.Minimum,
			"maximum":        constraint.Maximum,
			"allowed_values": constraint.AllowedValues,
		}
	}
	return map[string]any{"contract_version": d.ContractVersion, "paths": paths}
}

func (d *Document) EditablePaths() []string {
	paths := make([]string, 0, len(d.Paths))
	for path, constraint := range d.Paths {
		if constraint.Editable {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	return paths
}

func (d *Document) validateCompleteObject(field string, reference, actual map[string]any) (map[string]any, error) {
	for key := range actual {
		if _, ok := reference[key]; !ok {
			return nil, unknownPath(field + "." + key)
		}
	}
	completed := make(map[string]any, len(reference))
	for key, referenceValue := range reference {
		actualValue, ok := actual[key]
		if !ok {
			return nil, ValidationError{Code: "REQUEST_INVALID", Field: field + "." + key, Message: "Every editable shared parameter must be supplied."}
		}
		if nestedReference, nested := referenceValue.(map[string]any); nested {
			nestedActual, ok := actualValue.(map[string]any)
			if !ok {
				return nil, typeError(field + "." + key)
			}
			value, err := d.validateCompleteObject(field+"."+key, nestedReference, nestedActual)
			if err != nil {
				return nil, err
			}
			completed[key] = value
			continue
		}
		path := strings.TrimPrefix(field+"."+key, "shared_parameters.")
		if err := d.validateLeaf(path, field+"."+key, actualValue); err != nil {
			return nil, err
		}
		if !d.Paths[path].Editable && !valuesEqual(actualValue, referenceValue) {
			return nil, ValidationError{Code: "PARAMETER_NOT_ALLOWED", Field: field + "." + key, Message: "This fixed S1 parameter must retain its reference value."}
		}
		completed[key] = actualValue
	}
	return completed, nil
}

func (d *Document) validateSparseObject(field string, reference, actual map[string]any) (map[string]any, error) {
	result := make(map[string]any, len(actual))
	for key, value := range actual {
		referenceValue, ok := reference[key]
		if !ok {
			return nil, unknownPath(field + "." + key)
		}
		if nestedReference, nested := referenceValue.(map[string]any); nested {
			nestedActual, ok := value.(map[string]any)
			if !ok {
				return nil, typeError(field + "." + key)
			}
			nestedResult, err := d.validateSparseObject(field+"."+key, nestedReference, nestedActual)
			if err != nil {
				return nil, err
			}
			result[key] = nestedResult
			continue
		}
		path := strings.TrimPrefix(field+"."+key, agentParameterPrefix(field))
		if err := d.validateLeaf(path, field+"."+key, value); err != nil {
			return nil, err
		}
		result[key] = value
	}
	return result, nil
}

func agentParameterPrefix(field string) string {
	marker := ".parameters"
	index := strings.Index(field, marker)
	if index < 0 {
		return ""
	}
	return field[:index+len(marker)+1]
}

func (d *Document) validateLeaf(path, field string, value any) error {
	constraint, ok := d.Paths[path]
	if !ok {
		return unknownPath(field)
	}
	if !constraint.Editable && strings.Contains(field, ".parameters") {
		return ValidationError{Code: "PARAMETER_NOT_ALLOWED", Field: field, Message: "This fixed S1 parameter cannot be overridden for an Agent."}
	}
	if err := constraint.validateValue(value); err != nil {
		return ValidationError{Code: "PARAMETER_OUT_OF_RANGE", Field: field, Message: "Parameter value does not satisfy its declared constraint."}
	}
	return nil
}

func (constraint Constraint) validateDefinition() error {
	if constraint.Type != "integer" && constraint.Type != "number" && constraint.Type != "boolean" && constraint.Type != "string" {
		return fmt.Errorf("unsupported parameter type")
	}
	if constraint.Type != "integer" && constraint.Type != "number" && (constraint.Minimum != nil || constraint.Maximum != nil) {
		return fmt.Errorf("bounds are only valid for numeric parameters")
	}
	if constraint.Minimum != nil && (!isFinite(*constraint.Minimum) || constraint.Maximum != nil && *constraint.Minimum > *constraint.Maximum) {
		return fmt.Errorf("invalid minimum")
	}
	if constraint.Maximum != nil && !isFinite(*constraint.Maximum) {
		return fmt.Errorf("invalid maximum")
	}
	for _, value := range constraint.AllowedValues {
		if err := constraint.validateValueWithoutAllowed(value); err != nil || value == nil {
			return fmt.Errorf("allowed value has an invalid type")
		}
	}
	return nil
}

func (constraint Constraint) validateValue(value any) error {
	if err := constraint.validateValueWithoutAllowed(value); err != nil {
		return err
	}
	if value == nil || constraint.AllowedValues == nil {
		return nil
	}
	for _, allowed := range constraint.AllowedValues {
		if valuesEqual(value, allowed) {
			return nil
		}
	}
	return fmt.Errorf("value is not allowed")
}

func (constraint Constraint) validateValueWithoutAllowed(value any) error {
	if value == nil {
		if constraint.Nullable {
			return nil
		}
		return fmt.Errorf("null is not permitted")
	}
	switch constraint.Type {
	case "boolean":
		if _, ok := value.(bool); !ok {
			return fmt.Errorf("boolean required")
		}
		return nil
	case "string":
		if _, ok := value.(string); !ok {
			return fmt.Errorf("string required")
		}
		return nil
	case "integer", "number":
		numeric, ok := asNumber(value)
		if !ok || !isFinite(numeric) || constraint.Type == "integer" && math.Trunc(numeric) != numeric {
			return fmt.Errorf("finite %s required", constraint.Type)
		}
		if constraint.Minimum != nil && numeric < *constraint.Minimum {
			return fmt.Errorf("below minimum")
		}
		if constraint.Maximum != nil && numeric > *constraint.Maximum {
			return fmt.Errorf("above maximum")
		}
		return nil
	default:
		return fmt.Errorf("unsupported parameter type")
	}
}

func flattenReference(prefix string, value any, leaves map[string]any) error {
	object, ok := value.(map[string]any)
	if !ok {
		leaves[prefix] = value
		return nil
	}
	if len(object) == 0 {
		return fmt.Errorf("reference has an empty parameter group")
	}
	for key, nested := range object {
		path := key
		if prefix != "" {
			path = prefix + "." + key
		}
		if err := flattenReference(path, nested, leaves); err != nil {
			return err
		}
	}
	return nil
}

func referenceAgentSegments(reference []any) (map[int]string, error) {
	segments := make(map[int]string, len(reference))
	for _, raw := range reference {
		object, ok := raw.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("reference agents are invalid")
		}
		agent, ok := asInteger(object["agent"])
		segment, segmentOK := object["segment"].(string)
		if !ok || !segmentOK || agent < 1 || agent > 3 || segments[agent] != "" {
			return nil, fmt.Errorf("reference agents are invalid")
		}
		segments[agent] = segment
	}
	if len(segments) != 3 || segments[1] != "EARLY" || segments[2] != "MIDDLE" || segments[3] != "LATE" {
		return nil, fmt.Errorf("reference agents are invalid")
	}
	return segments, nil
}

func unknownPath(field string) ValidationError {
	return ValidationError{Code: "PARAMETER_NOT_ALLOWED", Field: field, Message: "Parameter path is not editable."}
}

func typeError(field string) ValidationError {
	return ValidationError{Code: "PARAMETER_OUT_OF_RANGE", Field: field, Message: "Parameter value has an invalid type."}
}

func asNumber(value any) (float64, bool) {
	switch typed := value.(type) {
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	default:
		return 0, false
	}
}

func asInteger(value any) (int, bool) {
	number, ok := asNumber(value)
	if !ok || !isFinite(number) || math.Trunc(number) != number || number < math.MinInt || number > math.MaxInt {
		return 0, false
	}
	return int(number), true
}

func valuesEqual(left, right any) bool {
	if leftNumber, leftOK := asNumber(left); leftOK {
		rightNumber, rightOK := asNumber(right)
		return rightOK && leftNumber == rightNumber
	}
	return left == right
}

func isFinite(value float64) bool { return !math.IsNaN(value) && !math.IsInf(value, 0) }
