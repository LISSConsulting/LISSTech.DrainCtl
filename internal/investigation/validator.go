//go:build windows

package investigation

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"regexp"
	"strings"
	"unicode/utf8"
)

var ipv4Literal = regexp.MustCompile(`(?:^|[^0-9])(?:[0-9]{1,3}\.){3}[0-9]{1,3}(?:$|[^0-9])`)

func ValidateReportJSON(raw []byte, factIDs, canaries []string) (Report, error) {
	if err := rejectDuplicateOrTrailing(raw); err != nil {
		return Report{}, err
	}
	var value any
	if err := json.Unmarshal(raw, &value); err != nil {
		return Report{}, fmt.Errorf("invalid report: %w", err)
	}
	if err := validateReportShape(value); err != nil {
		return Report{}, err
	}
	var report Report
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return Report{}, fmt.Errorf("invalid report: %w", err)
	}
	if err := ValidateReport(report, factIDs, canaries); err != nil {
		return Report{}, err
	}
	return report, nil
}

func rejectDuplicateOrTrailing(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := scanJSON(decoder); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		return errors.New("trailing JSON")
	}
	return nil
}

func scanJSON(decoder *json.Decoder) error {
	token, err := decoder.Token()
	if err != nil {
		return err
	}
	delimiter, isDelimiter := token.(json.Delim)
	if !isDelimiter {
		return nil
	}
	switch delimiter {
	case '{':
		seen := make(map[string]bool)
		for decoder.More() {
			key, err := decoder.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok || seen[name] {
				return errors.New("duplicate JSON key")
			}
			seen[name] = true
			if err := scanJSON(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	case '[':
		for decoder.More() {
			if err := scanJSON(decoder); err != nil {
				return err
			}
		}
		_, err := decoder.Token()
		return err
	default:
		return errors.New("invalid JSON delimiter")
	}
}

func validateReportShape(value any) error {
	report, ok := value.(map[string]any)
	if !ok || !exactObject(report, "result_version", "summary", "overall_assessment", "evidence_sufficiency", "human_review_required", "hypotheses", "missing_evidence", "recommended_diagnostic_checks") {
		return errors.New("invalid report shape")
	}
	if !isInteger(report["result_version"]) || !isString(report["overall_assessment"]) || !isString(report["evidence_sufficiency"]) || !isBool(report["human_review_required"]) {
		return errors.New("invalid report primitive")
	}
	summary, ok := report["summary"].(map[string]any)
	if !ok || !exactObject(summary, "text_kind", "text", "fact_ids") || !validateTextObject(summary, "untrusted_summary", "fact_ids") {
		return errors.New("invalid summary shape")
	}
	hypotheses, ok := report["hypotheses"].([]any)
	if !ok {
		return errors.New("invalid hypotheses shape")
	}
	for _, value := range hypotheses {
		hypothesis, ok := value.(map[string]any)
		if !ok || !exactObject(hypothesis, "rank", "confidence", "text_kind", "text", "supporting_fact_ids", "contradicting_fact_ids") || !isInteger(hypothesis["rank"]) || !isString(hypothesis["confidence"]) || !validateTextObject(value, "untrusted_hypothesis", "supporting_fact_ids", "contradicting_fact_ids") {
			return errors.New("invalid hypothesis shape")
		}
	}
	missing, ok := report["missing_evidence"].([]any)
	if !ok {
		return errors.New("invalid missing evidence shape")
	}
	for _, value := range missing {
		item, ok := value.(map[string]any)
		if !ok || !exactObject(item, "category", "text_kind", "text", "related_fact_ids") || !isString(item["category"]) || !validateTextObject(value, "untrusted_missing_evidence", "related_fact_ids") {
			return errors.New("invalid missing evidence item")
		}
	}
	checks, ok := report["recommended_diagnostic_checks"].([]any)
	if !ok {
		return errors.New("invalid diagnostic checks shape")
	}
	for _, value := range checks {
		check, ok := value.(map[string]any)
		if !ok || !exactObject(check, "rank", "check_type", "text_kind", "text", "fact_ids", "related_hypothesis_ranks") || !isInteger(check["rank"]) || !isString(check["check_type"]) || !validateTextObject(value, "untrusted_diagnostic_check", "fact_ids") || !integerArray(check["related_hypothesis_ranks"]) {
			return errors.New("invalid diagnostic check shape")
		}
	}
	return nil
}

func validateTextObject(value any, textKind string, factFields ...string) bool {
	object, ok := value.(map[string]any)
	if !ok || !isString(object["text_kind"]) || object["text_kind"] != textKind || !isString(object["text"]) {
		return false
	}
	for _, field := range factFields {
		if !stringArray(object[field]) {
			return false
		}
	}
	return true
}

func exactObject(object map[string]any, names ...string) bool {
	if len(object) != len(names) {
		return false
	}
	for _, name := range names {
		if _, ok := object[name]; !ok {
			return false
		}
	}
	return true
}

func isString(value any) bool { _, ok := value.(string); return ok }
func isBool(value any) bool   { _, ok := value.(bool); return ok }
func isInteger(value any) bool {
	number, ok := value.(float64)
	return ok && number == float64(int(number))
}
func stringArray(value any) bool {
	values, ok := value.([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		if !isString(value) {
			return false
		}
	}
	return true
}
func integerArray(value any) bool {
	values, ok := value.([]any)
	if !ok {
		return false
	}
	for _, value := range values {
		if !isInteger(value) {
			return false
		}
	}
	return true
}

func ValidateReport(report Report, factIDs, canaries []string) error {
	allowed := make(map[string]bool, len(factIDs))
	for _, id := range factIDs {
		allowed[id] = true
	}
	if len(allowed) == 0 || report.ResultVersion != ResultVersion {
		return errors.New("invalid report version or facts")
	}
	if report.Summary.TextKind != TextKindSummary || !validProse(report.Summary.Text, 1280, canaries) || !validFactIDs(report.Summary.FactIDs, allowed, 1, 12) {
		return errors.New("invalid summary")
	}
	if len(report.Hypotheses) > 5 || len(report.MissingEvidence) > 6 || len(report.RecommendedDiagnosticChecks) < 1 || len(report.RecommendedDiagnosticChecks) > 6 {
		return errors.New("invalid report collection")
	}
	insufficient := report.EvidenceSufficiency == EvidenceSufficiencyInsufficient
	if insufficient != (report.OverallAssessment == OverallAssessmentInsufficientEvidence) ||
		(insufficient && (!report.HumanReviewRequired || len(report.Hypotheses) != 0 || len(report.MissingEvidence) == 0)) ||
		(!insufficient && (len(report.Hypotheses) == 0 || !validAssessment(report.OverallAssessment) || (report.EvidenceSufficiency != EvidenceSufficiencyPartial && report.EvidenceSufficiency != EvidenceSufficiencySufficient))) {
		return errors.New("invalid evidence sufficiency")
	}

	ranks := make(map[int]bool, len(report.Hypotheses))
	for index, hypothesis := range report.Hypotheses {
		if hypothesis.Rank != index+1 || ranks[hypothesis.Rank] || !validConfidence(hypothesis.Confidence) || hypothesis.TextKind != TextKindHypothesis || !validProse(hypothesis.Text, 960, canaries) || !validFactIDs(hypothesis.SupportingFactIDs, allowed, 1, 12) || !validFactIDs(hypothesis.ContradictingFactIDs, allowed, 0, 12) || overlap(hypothesis.SupportingFactIDs, hypothesis.ContradictingFactIDs) {
			return errors.New("invalid hypothesis")
		}
		ranks[hypothesis.Rank] = true
	}
	for _, missing := range report.MissingEvidence {
		if missing.TextKind != TextKindMissingEvidence || !validCategory(missing.Category) || !validProse(missing.Text, 640, canaries) || !validFactIDs(missing.RelatedFactIDs, allowed, 0, 8) {
			return errors.New("invalid missing evidence")
		}
	}
	for index, check := range report.RecommendedDiagnosticChecks {
		if check.Rank != index+1 || !validCheckType(check.CheckType) || check.TextKind != TextKindDiagnosticCheck || !validProse(check.Text, 960, canaries) || !validFactIDs(check.FactIDs, allowed, 0, 8) || len(check.RelatedHypothesisRanks) > 5 {
			return errors.New("invalid diagnostic check")
		}
		seen := make(map[int]bool, len(check.RelatedHypothesisRanks))
		for _, rank := range check.RelatedHypothesisRanks {
			if !ranks[rank] || seen[rank] {
				return errors.New("invalid check hypothesis")
			}
			seen[rank] = true
		}
	}
	return nil
}

func validFactIDs(ids []string, allowed map[string]bool, minimum, maximum int) bool {
	if len(ids) < minimum || len(ids) > maximum {
		return false
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if !allowed[id] || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

func overlap(first, second []string) bool {
	seen := make(map[string]bool, len(first))
	for _, id := range first {
		seen[id] = true
	}
	for _, id := range second {
		if seen[id] {
			return true
		}
	}
	return false
}

func validProse(text string, maximum int, canaries []string) bool {
	if !utf8.ValidString(text) || len(text) == 0 || len(text) > maximum || strings.TrimSpace(text) != text {
		return false
	}
	for _, character := range text {
		if character < 0x20 || character > 0x7e || strings.ContainsRune("/\\<>@`", character) {
			return false
		}
	}
	if ipv4Literal.MatchString(text) || containsIPv6Literal(text) {
		return false
	}
	normalized := asciiLower(text)
	for _, canary := range canaries {
		if canary != "" && strings.Contains(normalized, asciiLower(canary)) {
			return false
		}
	}
	return true
}

func containsIPv6Literal(text string) bool {
	for _, token := range strings.FieldsFunc(text, func(character rune) bool {
		return character != ':' && character != '[' && character != ']' && character != '.' && character != '%' && (character < '0' || character > '9') && (character < 'a' || character > 'f') && (character < 'A' || character > 'F')
	}) {
		candidate := strings.Trim(token, "[]")
		if strings.Count(candidate, ":") >= 2 {
			if _, err := netip.ParseAddr(strings.Split(candidate, "%")[0]); err == nil {
				return true
			}
		}
	}
	return false
}

func asciiLower(value string) string {
	return strings.Map(func(character rune) rune {
		if character >= 'A' && character <= 'Z' {
			return character + ('a' - 'A')
		}
		return character
	}, value)
}

func validAssessment(value OverallAssessment) bool {
	return value == OverallAssessmentIndeterminate || value == OverallAssessmentLocalizedOperationalIssue || value == OverallAssessmentFleetWideOperationalIssue || value == OverallAssessmentExpectedOrMaintenanceRelated
}
func validConfidence(value Confidence) bool {
	return value == ConfidenceLow || value == ConfidenceMedium || value == ConfidenceHigh
}
func validCategory(value MissingEvidenceCategory) bool {
	switch value {
	case MissingEvidenceAdditionalTimeSeries, MissingEvidenceHostHealthDetail, MissingEvidenceServiceState, MissingEvidenceAuthenticationDetail, MissingEvidenceNetworkDependencyDetail, MissingEvidenceChangeMaintenanceContext, MissingEvidenceFleetComparison, MissingEvidenceOther:
		return true
	}
	return false
}
func validCheckType(value DiagnosticCheckType) bool {
	switch value {
	case DiagnosticCheckInspectRetainedMetrics, DiagnosticCheckVerifyServiceState, DiagnosticCheckVerifyAuthenticationState, DiagnosticCheckVerifyNetworkOrDependency, DiagnosticCheckVerifyChangeMaintenanceContext, DiagnosticCheckCompareFleet, DiagnosticCheckCollectAdditionalObservation:
		return true
	}
	return false
}
