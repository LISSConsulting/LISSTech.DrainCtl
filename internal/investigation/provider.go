//go:build windows

package investigation

import (
	"bytes"
	"compress/gzip"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	maxHeaders       = 16_384
	maxResponseBytes = 32_768
	maxDiscardBytes  = 4_096
)

// SendConfiguration is deliberately narrow: validation happens before decryption and
// the decrypted bytes stay within the provider send path.
type SendConfiguration interface {
	ValidateForSend(context.Context) error
	DecryptCredential(context.Context) ([]byte, error)
}

// SendAuthorization durably records that one provider transmission may begin.
// ProviderClient invokes it exactly once after the request is fully prepared and
// immediately before its sole Client.Do call.
type SendAuthorization func(context.Context) error

type ProviderClient struct {
	client    *http.Client
	transport *http.Transport
}

type ProviderOutcome struct {
	Report                                 *Report
	Reason                                 TerminalReason
	RequestHeaderBytes, RequestBodyBytes   int
	ResponseHeaderBytes, ResponseBodyBytes int
}

func NewProviderClient() *ProviderClient {
	return NewProviderClientWithTransport(&http.Transport{
		Proxy:           nil,
		TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12},
	})
}

// NewProviderClientWithTransport exists solely for non-dialling transport tests.
// The supplied transport is still forced to bypass every proxy.
func NewProviderClientWithTransport(transport *http.Transport) *ProviderClient {
	if transport == nil {
		transport = &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}}
	}
	transport.Proxy = nil
	if transport.TLSClientConfig == nil {
		transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	} else if transport.TLSClientConfig.MinVersion < tls.VersionTLS12 {
		transport.TLSClientConfig.MinVersion = tls.VersionTLS12
	}
	return &ProviderClient{
		transport: transport,
		client: &http.Client{
			Transport: transport,
			Timeout:   30 * time.Second,
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

func (p *ProviderClient) Transport() *http.Transport { return p.transport }

func (p *ProviderClient) Send(ctx context.Context, cfg SendConfiguration, evidence []byte, factIDs, canaries []string, authorize SendAuthorization) (out ProviderOutcome, returnErr error) {
	if cfg == nil {
		return ProviderOutcome{Reason: TerminalReasonConfigurationInvalid}, nil
	}
	if err := cfg.ValidateForSend(ctx); err != nil {
		return ProviderOutcome{Reason: TerminalReasonConfigurationDisabled}, nil
	}

	body, err := BuildRequest(evidence)
	if err != nil {
		return ProviderOutcome{Reason: TerminalReasonRequestLimit}, nil
	}
	credential, err := cfg.DecryptCredential(ctx)
	if err != nil || len(credential) == 0 {
		zero(credential)
		return ProviderOutcome{Reason: TerminalReasonConfigurationInvalid}, nil
	}

	resp, out, err := p.do(ctx, cfg, body, credential, authorize)
	if err != nil {
		return out, err
	}
	if resp == nil {
		return out, nil
	}
	defer func() {
		if err := resp.Body.Close(); err != nil && out.Reason == TerminalReasonNone && returnErr == nil {
			out.Reason = TerminalReasonResponseInvalid
		}
	}()

	out.ResponseHeaderBytes = headerBytes(resp.Header, "")
	if out.ResponseHeaderBytes > maxHeaders {
		out.Reason = TerminalReasonResponseLimit
		return out, nil
	}
	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		// Non-success handling is intentionally status-only. Consume a small bounded
		// amount only so the connection can be reused; no provider bytes are retained.
		_, _ = boundedResponse(resp, maxDiscardBytes)
		out.Reason = mapHTTPStatus(resp.StatusCode)
		return out, nil
	}

	data, err := boundedResponse(resp, maxResponseBytes)
	if err != nil {
		out.Reason = responseReason(err)
		return out, nil
	}
	out.ResponseBodyBytes = len(data)
	if !strings.HasPrefix(strings.ToLower(resp.Header.Get("Content-Type")), "application/json") {
		out.Reason = TerminalReasonResponseInvalid
		return out, nil
	}
	out.Report, out.Reason = parseEnvelope(data, factIDs, canaries)
	return out, nil
}

// do keeps the unavoidable immutable bearer value scoped to the request send.
// The Authorization header and all mutable credential storage are released before
// it returns a response to its caller.
func (p *ProviderClient) do(ctx context.Context, cfg SendConfiguration, body, credential []byte, authorize SendAuthorization) (*http.Response, ProviderOutcome, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, ProviderEndpoint, bytes.NewReader(body))
	if err != nil {
		zero(credential)
		return nil, ProviderOutcome{}, err
	}
	req.Header.Set("Authorization", "Bearer "+string(credential))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Accept-Encoding", "gzip")
	defer func() {
		req.Header.Del("Authorization")
		zero(credential)
		req = nil
	}()

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	out := ProviderOutcome{RequestBodyBytes: len(body), RequestHeaderBytes: headerBytes(req.Header, host)}
	if out.RequestHeaderBytes > maxHeaders {
		out.Reason = TerminalReasonRequestLimit
		return nil, out, nil
	}
	if err := cfg.ValidateForSend(ctx); err != nil {
		return nil, ProviderOutcome{Reason: TerminalReasonConfigurationDisabled}, nil
	}
	if authorize == nil {
		return nil, ProviderOutcome{Reason: TerminalReasonConfigurationInvalid}, nil
	}
	if err := authorize(ctx); err != nil {
		return nil, out, err
	}

	resp, err := p.client.Do(req)
	if err != nil {
		return resp, out, mapTransportError(ctx, err)
	}
	return resp, out, nil
}

func zero(bytes []byte) {
	for i := range bytes {
		bytes[i] = 0
	}
}

// headerBytes measures complete HTTP header fields, including the terminating
// blank line. host is supplied separately because net/http emits it from the
// request target rather than req.Header.
func headerBytes(headers http.Header, hosts ...string) int {
	host := ""
	if len(hosts) > 0 {
		host = hosts[0]
	}
	bytes := 2 // final CRLF
	if host != "" {
		bytes += len("Host") + len(host) + 4 // name, colon-space, CRLF
	}
	for name, values := range headers {
		for _, value := range values {
			bytes += len(name) + len(value) + 4 // name, colon-space, CRLF
		}
	}
	return bytes
}

var (
	errResponseLimit        = errors.New("response limit")
	errInvalidContentCoding = errors.New("invalid content encoding")
)

func boundedResponse(resp *http.Response, limit int) ([]byte, error) {
	var reader io.Reader = resp.Body
	switch strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding"))) {
	case "", "identity":
	case "gzip":
		gzipReader, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, errInvalidContentCoding
		}
		reader = gzipReader
		body, readErr := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
		closeErr := gzipReader.Close()
		if readErr != nil {
			return nil, readErr
		}
		if len(body) > limit {
			return nil, errResponseLimit
		}
		if closeErr != nil {
			return nil, closeErr
		}
		return body, nil
	default:
		return nil, errInvalidContentCoding
	}
	body, err := io.ReadAll(io.LimitReader(reader, int64(limit)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > limit {
		return nil, errResponseLimit
	}
	return body, nil
}

func responseReason(err error) TerminalReason {
	if errors.Is(err, errResponseLimit) {
		return TerminalReasonResponseLimit
	}
	return TerminalReasonResponseInvalid
}

func mapTransportError(ctx context.Context, err error) error {
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return timeoutError{}
	}
	return networkError{}
}

type timeoutError struct{}

func (timeoutError) Error() string { return "timeout" }

type networkError struct{}

func (networkError) Error() string { return "network" }

func TransportReason(err error) TerminalReason {
	if _, ok := err.(timeoutError); ok {
		return TerminalReasonTimeout
	}
	return TerminalReasonNetworkError
}

func mapHTTPStatus(status int) TerminalReason {
	switch {
	case status >= http.StatusMultipleChoices && status < http.StatusBadRequest:
		return TerminalReasonRedirectRefused
	case status == http.StatusUnauthorized || status == http.StatusForbidden:
		return TerminalReasonAuthenticationFailed
	case status == http.StatusBadRequest || status == http.StatusNotFound || status == http.StatusConflict || status == http.StatusUnprocessableEntity:
		return TerminalReasonProviderRequestRejected
	case status == http.StatusRequestEntityTooLarge:
		return TerminalReasonRequestLimit
	case status == http.StatusTooManyRequests:
		return TerminalReasonProviderRateLimited
	default:
		return TerminalReasonUpstreamError
	}
}

type requestContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}
type requestInput struct {
	Role    string           `json:"role"`
	Content []requestContent `json:"content"`
}
type responsesRequest struct {
	Model           string `json:"model"`
	Store           bool   `json:"store"`
	Background      bool   `json:"background"`
	Stream          bool   `json:"stream"`
	MaxOutputTokens int    `json:"max_output_tokens"`
	Reasoning       struct {
		Effort string `json:"effort"`
	} `json:"reasoning"`
	Truncation        string         `json:"truncation"`
	ParallelToolCalls bool           `json:"parallel_tool_calls"`
	ToolChoice        string         `json:"tool_choice"`
	Tools             []any          `json:"tools"`
	Input             []requestInput `json:"input"`
	Text              struct {
		Verbosity string `json:"verbosity"`
		Format    struct {
			Type   string          `json:"type"`
			Name   string          `json:"name"`
			Strict bool            `json:"strict"`
			Schema json.RawMessage `json:"schema"`
		} `json:"format"`
	} `json:"text"`
}

func BuildRequest(evidence []byte) ([]byte, error) {
	if len(evidence) == 0 || len(evidence) > maxEvidenceBytes {
		return nil, errors.New("invalid evidence")
	}
	request := responsesRequest{
		Model: ProviderModel, Store: false, Background: false, Stream: false,
		MaxOutputTokens: 8192, Truncation: "disabled", ParallelToolCalls: false,
		ToolChoice: "none", Tools: []any{},
		Input: []requestInput{
			{Role: "developer", Content: []requestContent{{Type: "input_text", Text: DeveloperInstruction}}},
			{Role: "user", Content: []requestContent{{Type: "input_text", Text: "EVIDENCE_JSON_V1\n" + string(evidence)}}},
		},
	}
	request.Reasoning.Effort = "high"
	request.Text.Verbosity = "low"
	request.Text.Format.Type = "json_schema"
	request.Text.Format.Name = string(ResponseFormat)
	request.Text.Format.Strict = true
	request.Text.Format.Schema = ReportSchema

	body, err := json.Marshal(request)
	if err != nil || len(body) > 16_384 {
		return nil, errors.New("request limit")
	}
	return body, nil
}

func parseEnvelope(raw []byte, facts, canaries []string) (*Report, TerminalReason) {
	if err := rejectDuplicateOrTrailing(raw); err != nil {
		return nil, TerminalReasonResponseInvalid
	}
	var envelope struct {
		Status            string            `json:"status"`
		Error             json.RawMessage   `json:"error"`
		IncompleteDetails json.RawMessage   `json:"incomplete_details"`
		Output            []json.RawMessage `json:"output"`
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if err := decoder.Decode(&envelope); err != nil {
		return nil, TerminalReasonResponseInvalid
	}
	if envelope.Error == nil {
		return nil, TerminalReasonResponseInvalid
	}
	if envelope.Status == "failed" || string(envelope.Error) != "null" {
		return nil, TerminalReasonUpstreamError
	}
	if envelope.Status == "incomplete" {
		return nil, TerminalReasonResponseIncomplete
	}
	if envelope.Status != "completed" || envelope.IncompleteDetails == nil || string(envelope.IncompleteDetails) != "null" {
		return nil, TerminalReasonResponseInvalid
	}

	var text string
	messages := 0
	for _, rawOutput := range envelope.Output {
		var kind struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(rawOutput, &kind); err != nil {
			return nil, TerminalReasonResponseInvalid
		}
		if kind.Type == "reasoning" {
			var reasoning struct {
				Type    string          `json:"type"`
				Summary json.RawMessage `json:"summary"`
			}
			decoder := json.NewDecoder(bytes.NewReader(rawOutput))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(&reasoning); err != nil ||
				(reasoning.Summary != nil && string(reasoning.Summary) != "[]") {
				return nil, TerminalReasonResponseInvalid
			}
			continue
		}
		var output struct {
			Type    string `json:"type"`
			Status  string `json:"status"`
			Role    string `json:"role"`
			Content []struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content"`
		}
		decoder := json.NewDecoder(bytes.NewReader(rawOutput))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&output); err != nil {
			return nil, TerminalReasonResponseInvalid
		}
		if output.Type != "message" || output.Status != "completed" || output.Role != "assistant" {
			return nil, TerminalReasonResponseInvalid
		}
		messages++
		if len(output.Content) != 1 {
			return nil, TerminalReasonResponseInvalid
		}
		if output.Content[0].Type == "refusal" {
			return nil, TerminalReasonProviderRefused
		}
		if output.Content[0].Type != "output_text" || output.Content[0].Text == "" {
			return nil, TerminalReasonResponseInvalid
		}
		text = output.Content[0].Text
	}
	if messages != 1 {
		return nil, TerminalReasonResponseInvalid
	}
	report, err := ValidateReportJSON([]byte(text), facts, canaries)
	if err != nil {
		return nil, TerminalReasonResponseInvalid
	}
	return &report, TerminalReasonNone
}
