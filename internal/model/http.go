package model

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"time"
	"unicode/utf8"

	c "github.com/ixayldz/Viber/internal/contracts"
	"github.com/ixayldz/Viber/internal/policy"
)

type Admission struct {
	Layers       []policy.Policy
	Epoch        int64
	Generation   int64
	InputBarrier bool
}
type Config struct {
	Authority        func(context.Context) (Admission, error)
	Provider         string
	Endpoint         string
	Timeout          time.Duration
	MaxResponseBytes int64
	Secret           func(context.Context) (string, error)
}
type Client struct {
	config Config
	http   *http.Client
}

func New(config Config) (*Client, error) {
	if config.Authority == nil {
		return nil, c.Fail(c.PolicyDenied, "trusted inference authority required")
	}
	u, err := url.Parse(config.Endpoint)
	if err != nil || u.User != nil || u.RawQuery != "" || u.Fragment != "" || u.Path != "" && u.Path != "/" {
		return nil, c.Fail(c.InvalidArgument, "provider origin required")
	}
	if config.Timeout <= 0 || config.Timeout > 5*time.Minute || config.MaxResponseBytes < 1024 || config.MaxResponseBytes > 8<<20 {
		return nil, c.Fail(c.InvalidArgument, "invalid provider resource profile")
	}
	switch config.Provider {
	case "openai", "chatgpt":
		if u.Scheme != "https" || u.Host != "api.openai.com" || config.Secret == nil {
			return nil, c.Fail(c.PolicyDenied, "official OpenAI origin and secret handle required")
		}
	case "anthropic":
		if u.Scheme != "https" || u.Host != "api.anthropic.com" || config.Secret == nil {
			return nil, c.Fail(c.PolicyDenied, "official Anthropic origin and secret handle required")
		}
	case "ollama":
		address, err := netip.ParseAddr(u.Hostname())
		if err != nil || !address.IsLoopback() || u.Scheme != "http" || config.Secret != nil {
			return nil, c.Fail(c.PolicyDenied, "local Ollama requires literal loopback without credentials")
		}
	default:
		return nil, c.Fail(c.UnsupportedCapability, "unknown model protocol")
	}
	dialer := net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}
	transport := &http.Transport{Proxy: nil, DisableCompression: true, TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12}, ResponseHeaderTimeout: 30 * time.Second, MaxResponseHeaderBytes: 64 << 10}
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		if host != u.Hostname() {
			return nil, c.Fail(c.PolicyDenied, "provider destination changed")
		}
		if config.Provider == "ollama" {
			return dialer.DialContext(ctx, network, address)
		}
		if port != "443" {
			return nil, c.Fail(c.PolicyDenied, "unexpected provider port")
		}
		ips, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
		if err != nil {
			return nil, err
		}
		for _, ip := range ips {
			ip = ip.Unmap()
			if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() {
				continue
			}
			connection, err := dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
			if err == nil {
				return connection, nil
			}
		}
		return nil, c.Fail(c.PolicyDenied, "provider has no allowed resolved address")
	}
	return &Client{config: config, http: &http.Client{Transport: transport, Timeout: config.Timeout, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}
func (client *Client) Close() {
	if transport, ok := client.http.Transport.(*http.Transport); ok {
		transport.CloseIdleConnections()
	}
}
func (client *Client) Complete(ctx context.Context, r Request) (result Result, callErr error) {
	dispatched := false
	defer func() {
		if callErr != nil && !dispatched {
			callErr = &PreflightFailure{Cause: callErr}
		}
	}()
	result = Result{SchemaVersion: 1, RequestID: r.ID, Provider: client.config.Provider, Model: r.Model, Calls: []Call{}, CompletionStatus: "UNKNOWN"}
	if err := validateRequest(r, client.config.Provider); err != nil {
		return result, err
	}
	if client.config.Provider == "ollama" && (strings.Contains(strings.ToLower(r.Model), ":cloud") || strings.HasSuffix(strings.ToLower(r.Model), "-cloud")) {
		return result, c.Fail(c.PolicyDenied, "cloud Ollama profiles are not local inference")
	}
	body, endpoint, err := encodeRequest(client.config.Provider, r)
	if err != nil {
		return result, err
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return result, err
	}
	if len(raw) > 8<<20 {
		return result, c.Fail(c.ContextTooSmall, "encoded model request exceeds transport quota")
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSuffix(client.config.Endpoint, "/")+endpoint, bytes.NewReader(raw))
	if err != nil {
		return result, err
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if client.config.Provider == "chatgpt" || r.Stream && client.config.Provider != "ollama" {
		request.Header.Set("Accept", "text/event-stream")
	} else if r.Stream && client.config.Provider == "ollama" {
		request.Header.Set("Accept", "application/x-ndjson")
	}
	// Reject stale policy before credential renewal/catalog network operations,
	// then recheck it immediately before the consequential inference request.
	if client.config.Secret != nil {
		admission, err := client.config.Authority(ctx)
		if err != nil {
			return result, err
		}
		if err = policy.Admit(admission.Layers, policy.Action{Epoch: admission.Epoch, Generation: admission.Generation, InputBarrier: admission.InputBarrier, Effect: "model.infer", Remote: true, Provider: client.config.Provider}); err != nil {
			return result, err
		}
		secret, err := client.config.Secret(ctx)
		if err != nil {
			return result, err
		}
		if secret == "" || strings.ContainsAny(secret, "\r\n") {
			return result, c.Fail(c.PolicyDenied, "unavailable scoped provider secret")
		}
		if client.config.Provider == "openai" || client.config.Provider == "chatgpt" {
			request.Header.Set("Authorization", "Bearer "+secret)
		} else {
			request.Header.Set("x-api-key", secret)
			request.Header.Set("anthropic-version", "2023-06-01")
		}
	}
	admission, err := client.config.Authority(ctx)
	if err != nil {
		return result, err
	}
	if err = policy.Admit(admission.Layers, policy.Action{Epoch: admission.Epoch, Generation: admission.Generation, InputBarrier: admission.InputBarrier, Effect: "model.infer", Remote: client.config.Provider != "ollama", Provider: client.config.Provider}); err != nil {
		return result, err
	}
	dispatched = true
	response, err := client.http.Do(request)
	if err != nil {
		kind := "TRANSPORT_UNKNOWN"
		if ctx.Err() != nil {
			kind = "CANCELLED_UNKNOWN"
		}
		// A possibly billed request is never transparently retried.
		return result, &Failure{Kind: kind, UsageUnknown: true}
	}
	defer response.Body.Close()
	result.ProviderRequestID = response.Header.Get("x-request-id")
	if result.ProviderRequestID == "" {
		result.ProviderRequestID = response.Header.Get("request-id")
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, client.config.MaxResponseBytes+1))
	result.Raw = bytes.Clone(data)
	if err != nil || int64(len(data)) > client.config.MaxResponseBytes {
		return result, &Failure{Kind: "INCOMPLETE_RESPONSE", UsageUnknown: true, ProviderRequestID: result.ProviderRequestID}
	}
	if response.StatusCode != 200 {
		// Error bodies may contain secrets/user material; preserve private raw bytes
		// but do not copy them into printable errors or provider fallbacks.
		return result, &Failure{Kind: "HTTP_ERROR", StatusCode: response.StatusCode, ProviderRequestID: result.ProviderRequestID, UsageUnknown: response.StatusCode >= 500, Retryable: false}
	}
	if err = decodeReceiptInto(r, client.config.Provider, data, &result); err != nil {
		return result, err
	}
	return result, nil
}

// DecodeReceipt revalidates retained wire bytes without dispatching any effect.
// It does not make provider-reported usage an independent task quality verdict.
func DecodeReceipt(r Request, provider string, data []byte) (Result, error) {
	result := Result{SchemaVersion: 1, RequestID: r.ID, Provider: provider, Model: r.Model, Raw: bytes.Clone(data)}
	if err := ValidateRequest(r, provider); err != nil {
		return result, err
	}
	err := decodeReceiptInto(r, provider, data, &result)
	return result, err
}
func decodeReceiptInto(r Request, provider string, data []byte, result *Result) error {
	if provider == "chatgpt" {
		return decodeChatGPTStream(r, data, result)
	}
	if r.Stream {
		return decodeStreamReceipt(r, provider, data, result)
	}
	if len(data) > 8<<20 || validateWire(data) != nil {
		return &Failure{Kind: "INVALID_RESPONSE", UsageUnknown: true}
	}
	if provider == "ollama" {
		wire, err := object(data)
		if err != nil {
			return err
		}
		if _, present := wire["model"]; present && text(wire, "model") != r.Model {
			return &Failure{Kind: "MODEL_PROFILE_MISMATCH", UsageUnknown: true, ProviderRequestID: result.ProviderRequestID}
		}
	}
	if err := decodeResult(provider, data, result); err != nil {
		result.Calls = nil
		return err
	}
	if err := validateResult(r, result); err != nil {
		result.Calls = nil
		result.Continuation = nil
		return err
	}
	return nil
}

// Wire JSON may contain provider metadata decimals. Kernel tool arguments and
// authoritative records still use canonical integer-only strict validation.
func validateWire(raw []byte) error {
	if !utf8.Valid(raw) {
		return errors.New("invalid UTF-8")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 64 {
			return errors.New("excessive nesting")
		}
		token, err := d.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for d.More() {
				key, err := d.Token()
				if err != nil {
					return err
				}
				text, ok := key.(string)
				if !ok || seen[text] {
					return errors.New("duplicate key")
				}
				seen[text] = true
				if err = value(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim('}') {
				return errors.New("invalid object")
			}
		case '[':
			for d.More() {
				if err = value(depth + 1); err != nil {
					return err
				}
			}
			end, err := d.Token()
			if err != nil || end != json.Delim(']') {
				return errors.New("invalid array")
			}
		default:
			return errors.New("invalid delimiter")
		}
		return nil
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := d.Token(); err != io.EOF {
		return errors.New("trailing content")
	}
	return nil
}
