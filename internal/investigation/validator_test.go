//go:build windows

package investigation

import (
	"strings"
	"testing"
)

func TestValidateReportJSONAcceptsOnlyCompleteClosedValidReport(t *testing.T) {
	report := validReportJSON()
	if _, err := ValidateReportJSON([]byte(report), validFacts(), nil); err != nil {
		t.Fatal(err)
	}
	for _, mutation := range []struct{ name, old, new string }{
		{"duplicate", `"result_version":1`, `"result_version":1,"result_version":1`},
		{"trailing", `}`, `} {}`},
		{"unknown report key", `"result_version":1,`, `"unexpected":true,"result_version":1,`},
		{"missing required field", `,"human_review_required":false`, ``},
		{"unknown nested key", `"text_kind":"untrusted_summary",`, `"unknown":true,"text_kind":"untrusted_summary",`},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := strings.Replace(report, mutation.old, mutation.new, 1)
			if _, err := ValidateReportJSON([]byte(candidate), validFacts(), nil); err == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
}

func TestValidateReportRejectsLexicalIdentityCitationAndRankViolations(t *testing.T) {
	for _, mutation := range []struct{ name, old, new string }{
		{"leading space", `Observed variation needs review`, ` Observed variation needs review`},
		{"line feed", `Observed variation needs review`, `Observed\nvariation needs review`},
		{"forbidden punctuation", `Observed variation needs review`, `Observed / variation`},
		{"ipv4", `Observed variation needs review`, `Address 198.51.100.47 needs review`},
		{"ipv6", `Observed variation needs review`, `Address [2001:db8:47::7] needs review`},
		{"unknown citation", `"fact_ids":["F001"]`, `"fact_ids":["F004"]`},
		{"duplicate citation", `"fact_ids":["F001"]`, `"fact_ids":["F001","F001"]`},
		{"nonconsecutive rank", `"rank":1,"confidence":"low"`, `"rank":2,"confidence":"low"`},
		{"overlapping facts", `"contradicting_fact_ids":[]`, `"contradicting_fact_ids":["F002"]`},
		{"unknown related hypothesis", `"related_hypothesis_ranks":[1]`, `"related_hypothesis_ranks":[2]`},
	} {
		t.Run(mutation.name, func(t *testing.T) {
			candidate := strings.Replace(validReportJSON(), mutation.old, mutation.new, 1)
			if _, err := ValidateReportJSON([]byte(candidate), validFacts(), nil); err == nil {
				t.Fatal("invalid report accepted")
			}
		})
	}
	if _, err := ValidateReportJSON([]byte(validReportJSON()), validFacts(), []string{"OBSERVED VARIATION"}); err == nil {
		t.Fatal("case-insensitive canary substring accepted")
	}
}

func TestValidateReportEnforcesTextByteBounds(t *testing.T) {
	tooLong := strings.Repeat("x", 1281)
	candidate := strings.Replace(validReportJSON(), "Observed variation needs review", tooLong, 1)
	if _, err := ValidateReportJSON([]byte(candidate), validFacts(), nil); err == nil {
		t.Fatal("overlength summary accepted")
	}
	candidate = strings.Replace(validReportJSON(), "Observed variation needs review", "Caf\u00e9", 1)
	if _, err := ValidateReportJSON([]byte(candidate), validFacts(), nil); err == nil {
		t.Fatal("non-ASCII prose accepted")
	}
}

func validReportJSON() string {
	return `{"result_version":1,"summary":{"text_kind":"untrusted_summary","text":"Observed variation needs review","fact_ids":["F001"]},"overall_assessment":"indeterminate","evidence_sufficiency":"partial","human_review_required":false,"hypotheses":[{"rank":1,"confidence":"low","text_kind":"untrusted_hypothesis","text":"Observed condition needs review","supporting_fact_ids":["F002"],"contradicting_fact_ids":[]}],"missing_evidence":[],"recommended_diagnostic_checks":[{"rank":1,"check_type":"inspect_retained_metrics","text_kind":"untrusted_diagnostic_check","text":"Inspect retained metrics","fact_ids":["F003"],"related_hypothesis_ranks":[1]}]}`
}
