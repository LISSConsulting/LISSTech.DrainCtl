//go:build windows

package investigation

import (
	"encoding/json"
	"testing"
)

func TestReportSchemaIsClosedAndRequiresEveryReportProperty(t *testing.T) {
	var schema map[string]any
	if err := json.Unmarshal(ReportSchema, &schema); err != nil {
		t.Fatal(err)
	}
	assertClosedSchemaObject(t, schema, []string{"result_version", "summary", "overall_assessment", "evidence_sufficiency", "human_review_required", "hypotheses", "missing_evidence", "recommended_diagnostic_checks"})
	definitions := schema["$defs"].(map[string]any)
	assertClosedSchemaObject(t, definitions["summary"].(map[string]any), []string{"text_kind", "text", "fact_ids"})
	assertClosedSchemaObject(t, definitions["hypothesis"].(map[string]any), []string{"rank", "confidence", "text_kind", "text", "supporting_fact_ids", "contradicting_fact_ids"})
	assertClosedSchemaObject(t, definitions["missing"].(map[string]any), []string{"category", "text_kind", "text", "related_fact_ids"})
	assertClosedSchemaObject(t, definitions["check"].(map[string]any), []string{"rank", "check_type", "text_kind", "text", "fact_ids", "related_hypothesis_ranks"})

	check := definitions["check"].(map[string]any)["properties"].(map[string]any)
	assertSchemaConstant(t, check["text_kind"], "untrusted_diagnostic_check")
	assertSchemaEnum(t, check["check_type"], []string{
		"inspect_retained_metrics", "verify_service_state", "verify_authentication_state",
		"verify_network_or_dependency", "verify_change_or_maintenance_context", "compare_fleet",
		"collect_additional_observation",
	})
}

func assertClosedSchemaObject(t *testing.T, schema map[string]any, expected []string) {
	t.Helper()
	if closed, ok := schema["additionalProperties"].(bool); !ok || closed {
		t.Fatalf("schema permits additional properties: %#v", schema)
	}
	required, ok := schema["required"].([]any)
	if !ok || len(required) != len(expected) {
		t.Fatalf("unexpected required fields: %#v", schema["required"])
	}
	seen := make(map[string]bool, len(required))
	for _, field := range required {
		name, ok := field.(string)
		if !ok {
			t.Fatalf("non-string required field: %#v", field)
		}
		seen[name] = true
	}
	properties := schema["properties"].(map[string]any)
	if len(properties) != len(expected) {
		t.Fatalf("unexpected properties: %#v", properties)
	}
	for _, field := range expected {
		if !seen[field] {
			t.Fatalf("missing required field %q", field)
		}
		if _, ok := properties[field]; !ok {
			t.Fatalf("missing property %q", field)
		}
	}
}

func assertSchemaEnum(t *testing.T, value any, expected []string) {
	t.Helper()
	entry := value.(map[string]any)
	values := entry["enum"].([]any)
	if len(values) != len(expected) {
		t.Fatalf("unexpected enum %#v", values)
	}
	for index, expectedValue := range expected {
		if values[index] != expectedValue {
			t.Fatalf("enum[%d] = %v, want %q", index, values[index], expectedValue)
		}
	}
}

func assertSchemaConstant(t *testing.T, value any, expected string) {
	t.Helper()
	entry := value.(map[string]any)
	if entry["const"] != expected {
		t.Fatalf("constant = %v, want %q", entry["const"], expected)
	}
}
