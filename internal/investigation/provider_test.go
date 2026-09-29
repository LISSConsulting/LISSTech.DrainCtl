//go:build windows

package investigation

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

type testConfiguration struct {
	validateErr error
	validate    func(int) error
	decryptErr  error
	credential  []byte
	validated   int
	decrypted   int
}

func (c *testConfiguration) ValidateForSend(context.Context) error {
	c.validated++
	if c.validate != nil {
		return c.validate(c.validated)
	}
	return c.validateErr
}
func (c *testConfiguration) DecryptCredential(context.Context) ([]byte, error) {
	c.decrypted++
	return c.credential, c.decryptErr
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return f(request) }

type verifyingBody struct {
	reader io.Reader
	verify func()
}

func (b *verifyingBody) Read(p []byte) (int, error) {
	b.verify()
	return b.reader.Read(p)
}

func (b *verifyingBody) Close() error { return nil }

func authorizeTestSend(context.Context) error { return nil }

func TestBuildRequestUsesFixedResponsesContractAndOrder(t *testing.T) {
	evidence := []byte(`{"v":1}`)
	body, err := BuildRequest(evidence)
	if err != nil {
		t.Fatal(err)
	}
	ordered := []string{"\"model\"", "\"store\"", "\"background\"", "\"stream\"", "\"max_output_tokens\"", "\"reasoning\"", "\"truncation\"", "\"parallel_tool_calls\"", "\"tool_choice\"", "\"tools\"", "\"input\"", "\"text\""}
	last := -1
	for _, key := range ordered {
		if index := bytes.Index(body, []byte(key)); index <= last {
			t.Fatalf("request key %s was not ordered: %s", key, body)
		} else {
			last = index
		}
	}
	var request struct {
		Model      string `json:"model"`
		Store      bool   `json:"store"`
		Background bool   `json:"background"`
		Stream     bool   `json:"stream"`
		Max        int    `json:"max_output_tokens"`
		Reasoning  struct {
			Effort string `json:"effort"`
		} `json:"reasoning"`
		Truncation string `json:"truncation"`
		Parallel   bool   `json:"parallel_tool_calls"`
		ToolChoice string `json:"tool_choice"`
		Tools      []any  `json:"tools"`
		Input      []struct {
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		} `json:"input"`
		Text struct {
			Format struct {
				Type   string          `json:"type"`
				Name   string          `json:"name"`
				Strict bool            `json:"strict"`
				Schema json.RawMessage `json:"schema"`
			} `json:"format"`
		} `json:"text"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		t.Fatal(err)
	}
	if request.Model != ProviderModel || request.Store || request.Background || request.Stream || request.Max != 8192 || request.Reasoning.Effort != "high" || request.Truncation != "disabled" || request.Parallel || request.ToolChoice != "none" || len(request.Tools) != 0 {
		t.Fatalf("unexpected fixed request: %+v", request)
	}
	if len(request.Input) != 2 || request.Input[1].Content[0].Text != "EVIDENCE_JSON_V1\n"+string(evidence) || strings.Count(request.Input[1].Content[0].Text, string(evidence)) != 1 {
		t.Fatalf("evidence was not appended exactly once: %#v", request.Input)
	}
	if request.Text.Format.Type != "json_schema" || request.Text.Format.Name != string(ResponseFormat) || !request.Text.Format.Strict {
		t.Fatalf("unexpected text format: %#v", request.Text.Format)
	}
	if len(body) > 16_384 {
		t.Fatalf("request exceeds limit: %d", len(body))
	}
}

func TestProviderRestrictsCredentialLifetimeToRequestSend(t *testing.T) {
	blocked := &testConfiguration{validateErr: errors.New("disabled"), credential: []byte("unread")}
	client := newSyntheticClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("request made after configuration rejection")
		return nil, nil
	})
	outcome, err := client.Send(context.Background(), blocked, []byte(`{"v":1}`), validFacts(), nil, authorizeTestSend)
	if err != nil || outcome.Reason != TerminalReasonConfigurationDisabled || blocked.validated != 1 || blocked.decrypted != 0 {
		t.Fatalf("configuration check did not precede decrypt: outcome=%+v err=%v validates=%d decrypts=%d", outcome, err, blocked.validated, blocked.decrypted)
	}

	credential := []byte("temporary-secret")
	ready := &testConfiguration{credential: credential}
	client = newSyntheticClient(t, func(request *http.Request) (*http.Response, error) {
		if ready.validated != 2 || ready.decrypted != 1 {
			t.Fatal("request did not follow initial and final validation plus one decrypt")
		}
		if string(credential) != "temporary-secret" {
			t.Fatal("credential was cleared before wire send")
		}
		if request.Method != http.MethodPost || request.URL.String() != ProviderEndpoint {
			t.Fatalf("unexpected endpoint %s %s", request.Method, request.URL)
		}
		if got := request.Header.Values("Authorization"); len(got) != 1 || got[0] != "Bearer temporary-secret" {
			t.Fatalf("authorization was not present exactly once during wire send: %#v", got)
		}
		for key, want := range map[string]string{"Content-Type": "application/json", "Accept": "application/json", "Accept-Encoding": "gzip"} {
			if request.Header.Get(key) != want {
				t.Fatalf("%s = %q", key, request.Header.Get(key))
			}
		}
		headers := make(http.Header)
		headers.Set("Content-Type", "application/json")
		headers.Set("Content-Encoding", "identity")
		return &http.Response{
			StatusCode: http.StatusOK,
			Header:     headers,
			Body: &verifyingBody{
				reader: bytes.NewReader(completedEnvelope(t)),
				verify: func() {
					if string(credential) != strings.Repeat("\x00", len(credential)) {
						t.Fatal("credential was reachable while response body was read")
					}
					if got := request.Header.Values("Authorization"); len(got) != 0 {
						t.Fatalf("authorization was retained during response body processing: %#v", got)
					}
				},
			},
		}, nil
	})
	outcome, err = client.Send(context.Background(), ready, []byte(`{"v":1}`), validFacts(), nil, authorizeTestSend)
	if err != nil || outcome.Reason != TerminalReasonNone || outcome.Report == nil || ready.decrypted != 1 {
		t.Fatalf("unexpected send result: %+v err=%v decrypts=%d", outcome, err, ready.decrypted)
	}
	requestHeaders := "Host: api.openai.com\r\nAuthorization: Bearer temporary-secret\r\nContent-Type: application/json\r\nAccept: application/json\r\nAccept-Encoding: gzip\r\n\r\n"
	if outcome.RequestHeaderBytes != len(requestHeaders) || outcome.ResponseHeaderBytes != len("Content-Type: application/json\r\nContent-Encoding: identity\r\n\r\n") {
		t.Fatalf("header accounting = request:%d response:%d", outcome.RequestHeaderBytes, outcome.ResponseHeaderBytes)
	}
}

func TestProviderAuthorizesOnlyAfterFinalValidationAndPreparedRequest(t *testing.T) {
	events := make([]string, 0, 3)
	credential := []byte("temporary-secret")
	config := &testConfiguration{
		credential: credential,
		validate: func(call int) error {
			events = append(events, "validate")
			if call == 2 {
				return errors.New("configuration changed")
			}
			return nil
		},
	}
	client := newSyntheticClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("request made after final configuration rejection")
		return nil, nil
	})

	outcome, err := client.Send(context.Background(), config, []byte(`{"v":1}`), validFacts(), nil, func(context.Context) error {
		events = append(events, "authorize")
		return nil
	})
	if err != nil || outcome.Reason != TerminalReasonConfigurationDisabled {
		t.Fatalf("final configuration rejection = (%+v, %v)", outcome, err)
	}
	if config.decrypted != 1 || config.validated != 2 || string(credential) != strings.Repeat("\x00", len(credential)) {
		t.Fatalf("final validation lifecycle = validates:%d decrypts:%d credential:%q", config.validated, config.decrypted, credential)
	}
	if got, want := strings.Join(events, ","), "validate,validate"; got != want {
		t.Fatalf("authorization ran before final configuration validation: %s", got)
	}
}

func TestProviderDoesNotSendWhenAuthorizationFails(t *testing.T) {
	credential := []byte("temporary-secret")
	authorizationErr := errors.New("durable authorization unavailable")
	client := newSyntheticClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("request made after authorization failure")
		return nil, nil
	})

	_, err := client.Send(context.Background(), &testConfiguration{credential: credential}, []byte(`{"v":1}`), validFacts(), nil, func(context.Context) error {
		return authorizationErr
	})
	if !errors.Is(err, authorizationErr) {
		t.Fatalf("authorization error = %v, want %v", err, authorizationErr)
	}
	if string(credential) != strings.Repeat("\x00", len(credential)) {
		t.Fatalf("credential retained after authorization failure: %q", credential)
	}
}

func TestProviderDoReturnsNilErrorAfterSuccessfulClientDo(t *testing.T) {
	client := newSyntheticClient(t, func(*http.Request) (*http.Response, error) {
		return response(http.StatusOK, "application/json", "identity", completedEnvelope(t)), nil
	})
	resp, _, err := client.do(context.Background(), &testConfiguration{}, []byte(`{}`), []byte("secret"), authorizeTestSend)
	if err != nil {
		t.Fatalf("successful Client.Do error = %v", err)
	}
	if resp == nil {
		t.Fatal("successful Client.Do returned no response")
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("closing response: %v", err)
	}
}

func TestProviderHeaderAccountingIncludesHostAndFinalCRLF(t *testing.T) {
	headers := http.Header{"X-Test": []string{"value"}}
	if got, want := headerBytes(headers, "example.test"), len("Host: example.test\r\nX-Test: value\r\n\r\n"); got != want {
		t.Fatalf("header bytes = %d, want %d", got, want)
	}

	for _, test := range []struct {
		name   string
		over   int
		reason TerminalReason
	}{
		{"at limit", 0, TerminalReasonNone},
		{"over limit", 1, TerminalReasonResponseLimit},
	} {
		t.Run(test.name, func(t *testing.T) {
			headers := http.Header{"Content-Type": []string{"application/json"}, "Content-Encoding": []string{"identity"}}
			valueLength := maxHeaders - headerBytes(headers, "") - len("X-Boundary") - 4 + test.over
			headers.Set("X-Boundary", strings.Repeat("x", valueLength))
			if got, want := headerBytes(headers, ""), maxHeaders+test.over; got != want {
				t.Fatalf("constructed header bytes = %d, want %d", got, want)
			}
			client := newSyntheticClient(t, func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: http.StatusOK, Header: headers, Body: io.NopCloser(bytes.NewReader(completedEnvelope(t)))}, nil
			})
			outcome, err := client.Send(context.Background(), &testConfiguration{credential: []byte("secret")}, []byte(`{"v":1}`), validFacts(), nil, authorizeTestSend)
			if err != nil || outcome.Reason != test.reason || outcome.ResponseHeaderBytes != maxHeaders+test.over {
				t.Fatalf("outcome=%+v err=%v", outcome, err)
			}
		})
	}
}

func TestProviderRejectsRequestHeadersOverExactBound(t *testing.T) {
	client := newSyntheticClient(t, func(*http.Request) (*http.Response, error) {
		t.Fatal("request made after request-header limit rejection")
		return nil, nil
	})
	outcome, err := client.Send(context.Background(), &testConfiguration{credential: bytes.Repeat([]byte("x"), maxHeaders)}, []byte(`{"v":1}`), validFacts(), nil, authorizeTestSend)
	if err != nil || outcome.Reason != TerminalReasonRequestLimit || outcome.RequestHeaderBytes <= maxHeaders {
		t.Fatalf("outcome=%+v err=%v", outcome, err)
	}
}

func TestParseEnvelopeAcceptsAdditiveEnvelopeAndRejectsNonMessageFields(t *testing.T) {
	additive := bytes.Replace(completedEnvelope(t), []byte(`{"status"`), []byte(`{"id":"resp_123","model":"gpt-6-astra","usage":{"input_tokens":1},"status"`), 1)
	if report, reason := parseEnvelope(additive, validFacts(), nil); reason != TerminalReasonNone || report == nil {
		t.Fatalf("additive envelope = (%#v, %s), want accepted report", report, reason)
	}

	withUnexpectedMessageField := []byte(`{"status":"completed","error":null,"incomplete_details":null,"output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":` + strconvQuote(validReportJSON()) + `}],"unexpected":true}]}`)
	if report, reason := parseEnvelope(withUnexpectedMessageField, validFacts(), nil); report != nil || reason != TerminalReasonResponseInvalid {
		t.Fatalf("non-strict message = (%#v, %s), want response_invalid", report, reason)
	}
}

func TestProviderMapsBoundedResponsesAndStatusOnlyFailures(t *testing.T) {
	cases := []struct {
		name                  string
		status                int
		contentType, encoding string
		body                  []byte
		reason                TerminalReason
	}{
		{"completed", 200, "application/json", "identity", completedEnvelope(t), TerminalReasonNone},
		{"refusal", 200, "application/json", "identity", []byte(`{"status":"completed","error":null,"incomplete_details":null,"output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"refusal"}]}]}`), TerminalReasonProviderRefused},
		{"incomplete", 200, "application/json", "identity", []byte(`{"status":"incomplete","error":null,"incomplete_details":{},"output":[]}`), TerminalReasonResponseIncomplete},
		{"failed", 200, "application/json", "identity", []byte(`{"status":"failed","error":{},"incomplete_details":null,"output":[]}`), TerminalReasonUpstreamError},
		{"missing-null", 200, "application/json", "identity", []byte(`{"status":"completed","error":null,"output":[]}`), TerminalReasonResponseInvalid},
		{"tool", 200, "application/json", "identity", []byte(`{"status":"completed","error":null,"incomplete_details":null,"output":[{"type":"function_call"}]}`), TerminalReasonResponseInvalid},
		{"non-json", 200, "text/plain", "identity", []byte("not json"), TerminalReasonResponseInvalid},
		{"redirect", 302, "application/json", "identity", []byte(`{"error":"ignored"}`), TerminalReasonRedirectRefused},
		{"rate", 429, "application/json", "identity", []byte(`{"error":"ignored"}`), TerminalReasonProviderRateLimited},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			client := newSyntheticClient(t, func(*http.Request) (*http.Response, error) {
				return response(test.status, test.contentType, test.encoding, test.body), nil
			})
			outcome, err := client.Send(context.Background(), &testConfiguration{credential: []byte("secret")}, []byte(`{"v":1}`), validFacts(), nil, authorizeTestSend)
			if err != nil || outcome.Reason != test.reason {
				t.Fatalf("outcome=%+v err=%v", outcome, err)
			}
		})
	}
	compressed := gzipBytes(t, bytes.Repeat([]byte("x"), maxResponseBytes+1))
	client := newSyntheticClient(t, func(*http.Request) (*http.Response, error) {
		return response(200, "application/json", "gzip", compressed), nil
	})
	outcome, err := client.Send(context.Background(), &testConfiguration{credential: []byte("secret")}, []byte(`{"v":1}`), validFacts(), nil, authorizeTestSend)
	if err != nil || outcome.Reason != TerminalReasonResponseLimit {
		t.Fatalf("gzip limit outcome=%+v err=%v", outcome, err)
	}
}

func newSyntheticClient(t *testing.T, roundTrip roundTripFunc) *ProviderClient {
	t.Helper()
	client := NewProviderClient()
	client.client = &http.Client{Transport: roundTrip, Timeout: 30 * 1000 * 1000 * 1000, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	return client
}

func response(status int, contentType, encoding string, body []byte) *http.Response {
	headers := make(http.Header)
	headers.Set("Content-Type", contentType)
	headers.Set("Content-Encoding", encoding)
	return &http.Response{StatusCode: status, Header: headers, Body: io.NopCloser(bytes.NewReader(body))}
}

func gzipBytes(t *testing.T, body []byte) []byte {
	t.Helper()
	var compressed bytes.Buffer
	writer := gzip.NewWriter(&compressed)
	if _, err := writer.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return compressed.Bytes()
}

func validFacts() []string { return []string{"F001", "F002", "F003"} }

func completedEnvelope(t *testing.T) []byte {
	t.Helper()
	report := `{"result_version":1,"summary":{"text_kind":"untrusted_summary","text":"Observed variation needs review","fact_ids":["F001"]},"overall_assessment":"indeterminate","evidence_sufficiency":"partial","human_review_required":false,"hypotheses":[{"rank":1,"confidence":"low","text_kind":"untrusted_hypothesis","text":"Observed condition needs review","supporting_fact_ids":["F002"],"contradicting_fact_ids":[]}],"missing_evidence":[],"recommended_diagnostic_checks":[{"rank":1,"check_type":"inspect_retained_metrics","text_kind":"untrusted_diagnostic_check","text":"Inspect retained metrics","fact_ids":["F003"],"related_hypothesis_ranks":[1]}]}`
	return []byte(`{"status":"completed","error":null,"incomplete_details":null,"output":[{"type":"message","status":"completed","role":"assistant","content":[{"type":"output_text","text":` + strconvQuote(report) + `}]}]}`)
}

func strconvQuote(value string) string { encoded, _ := json.Marshal(value); return string(encoded) }
