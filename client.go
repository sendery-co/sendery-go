// Package sendery sends transactional email with published Sendery templates.
package sendery

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	mathrand "math/rand/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const defaultBaseURL = "https://sendery.co"

var idempotencyKeyPattern = regexp.MustCompile(`^[a-zA-Z0-9_.:-]{1,128}$`)

// Attachment contains file bytes, which encoding/json encodes as base64.
// Files are sent with this request, not saved in email history.
type Attachment struct {
	Filename    string `json:"filename"`
	Content     []byte `json:"content"`
	ContentType string `json:"content_type,omitempty"`
}

// SendEmailInput identifies a published template, its recipient, and variables.
type SendEmailInput struct {
	Attachments []Attachment   `json:"attachments,omitempty"`
	To          string         `json:"to"`
	Template    string         `json:"template"`
	Data        map[string]any `json:"data"`
	Locale      string         `json:"locale,omitempty"`
}

// SendReceipt contains an email's ID and current delivery status.
// Acceptance does not confirm inbox delivery; use Client.Get to check status.
type SendReceipt struct {
	ID          string  `json:"id"`
	Status      string  `json:"status"`
	ErrorCode   *string `json:"error_code"`
	CreatedAt   string  `json:"created_at"`
	SubmittedAt *string `json:"submitted_at"`
}

// APIError describes an API failure. Status is zero for connection failures or
// unreadable successful responses. RetryAfter is nil when no delay was supplied.
type APIError struct {
	Status     int
	Code       string
	Errors     map[string][]string
	RetryAfter *time.Duration
	cause      error
}

func (e *APIError) Error() string {
	return fmt.Sprintf("sendery: %s (status %d)", e.Code, e.Status)
}

// Unwrap returns the underlying connection or decoding error, when present.
func (e *APIError) Unwrap() error { return e.cause }

// Retryable reports whether the same email may be retried after a temporary failure.
func (e *APIError) Retryable() bool {
	switch e.Status {
	case 0, 500, 502, 503, 504:
		return true
	case http.StatusTooManyRequests:
		return e.Code == "rate_limited"
	default:
		return false
	}
}

// Client sends requests to Sendery. A client can be shared by multiple goroutines.
type Client struct {
	apiKey  string
	baseURL string
	http    *http.Client
}

// ClientOption configures a Client before its first request.
type ClientOption func(*Client) error

// NewClient creates a client with a ten-second request timeout and no automatic
// retries. Store the project API key on your server.
func NewClient(apiKey string, options ...ClientOption) (*Client, error) {
	if strings.TrimSpace(apiKey) == "" || strings.ContainsAny(apiKey, "\r\n") {
		return nil, errors.New("sendery: provide a project API key")
	}
	client := &Client{
		apiKey: apiKey, baseURL: defaultBaseURL,
		http: &http.Client{Timeout: 10 * time.Second},
	}
	for _, option := range options {
		if err := option(client); err != nil {
			return nil, err
		}
	}
	client.http.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return client, nil
}

// WithBaseURL changes the server origin, without /api/v1. HTTPS is required,
// except for HTTP on localhost, 127.0.0.1, or ::1 during local testing.
func WithBaseURL(baseURL string) ClientOption {
	return func(client *Client) error {
		parsed, err := url.Parse(baseURL)
		if err != nil {
			return errors.New("sendery: provide an HTTPS base URL")
		}
		host := parsed.Hostname()
		loopback := host == "localhost" || host == "127.0.0.1" || host == "::1"
		if host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.ForceQuery || parsed.Fragment != "" ||
			(parsed.Scheme != "https" && !(parsed.Scheme == "http" && loopback)) {
			return errors.New("sendery: provide an HTTPS base URL without credentials, query, or fragment (HTTP allowed only on loopback)")
		}
		client.baseURL = strings.TrimRight(baseURL, "/")
		return nil
	}
}

// WithHTTPClient uses a copy of the supplied HTTP client. Redirects are disabled;
// a zero timeout is replaced with the default ten-second timeout.
func WithHTTPClient(httpClient *http.Client) ClientOption {
	return func(client *Client) error {
		if httpClient == nil || httpClient.Timeout < 0 {
			return errors.New("sendery: provide an HTTP client with a non-negative timeout")
		}
		copy := *httpClient
		if copy.Timeout == 0 {
			copy.Timeout = 10 * time.Second
		}
		client.http = &copy
		return nil
	}
}

// PendingEmail holds a serialized payload and a stable idempotency key. It can
// be sent again without changing either, and is safe for concurrent use.
type PendingEmail struct {
	client  *Client
	body    []byte
	key     string
	retries int
}

// SendOption configures a send or prepared email.
type SendOption func(*PendingEmail) error

// WithIdempotencyKey reuses the supplied key instead of generating one. Use a
// unique key for each email and keep that key and payload unchanged on retries.
func WithIdempotencyKey(key string) SendOption {
	return func(email *PendingEmail) error {
		if !idempotencyKeyPattern.MatchString(key) {
			return errors.New("sendery: idempotency keys must contain 1 to 128 letters, digits, underscores, periods, colons, or hyphens")
		}
		email.key = key
		return nil
	}
}

// WithRetries allows zero to five additional attempts for temporary failures.
// Retries use the same payload and key and honor Retry-After up to 30 seconds.
func WithRetries(retries int) SendOption {
	return func(email *PendingEmail) error {
		if retries < 0 || retries > 5 {
			return errors.New("sendery: choose 0 to 5 retries")
		}
		email.retries = retries
		return nil
	}
}

// Prepare freezes an email's payload and idempotency key without sending it.
// A nil Data map is sent as an empty JSON object.
func (c *Client) Prepare(input SendEmailInput, options ...SendOption) (*PendingEmail, error) {
	if input.Data == nil {
		input.Data = map[string]any{}
	}
	total := 0
	for _, file := range input.Attachments {
		if len(file.Content) == 0 {
			return nil, errors.New("sendery: attachments must not be empty")
		}
		total += len(file.Content)
	}
	if len(input.Attachments) > 10 || total > 5242880 {
		return nil, errors.New("sendery: use at most 10 attachments, up to 5 MB combined")
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, fmt.Errorf("sendery: encode email data: %w", err)
	}
	email := &PendingEmail{client: c, body: body, key: rand.Text()}
	for _, option := range options {
		if err := option(email); err != nil {
			return nil, err
		}
	}
	return email, nil
}

// Send prepares and sends an email. Each call generates a new key unless
// WithIdempotencyKey is provided; retries are opt-in through WithRetries.
func (c *Client) Send(ctx context.Context, input SendEmailInput, options ...SendOption) (*SendReceipt, error) {
	email, err := c.Prepare(input, options...)
	if err != nil {
		return nil, err
	}
	return email.Send(ctx)
}

// Get retrieves an email's latest delivery status by its returned ID.
func (c *Client) Get(ctx context.Context, id string) (*SendReceipt, error) {
	if id == "" {
		return nil, errors.New("sendery: provide an email ID")
	}
	return c.request(ctx, http.MethodGet, "/api/v1/emails/"+url.PathEscape(id), nil, "")
}

// Version returns a copy pinned to a published template version.
func (e *PendingEmail) Version(version int) (*PendingEmail, error) {
	if version < 1 {
		return nil, errors.New("sendery: version must be a positive integer")
	}
	var payload map[string]json.RawMessage
	if err := json.Unmarshal(e.body, &payload); err != nil {
		return nil, err
	}
	payload["version"] = json.RawMessage(strconv.Itoa(version))
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	pinned := *e
	pinned.body = body
	return &pinned, nil
}

// IdempotencyKey returns the key used for every attempt of this email.
func (e *PendingEmail) IdempotencyKey() string { return e.key }

// Send submits the prepared email. Canceling ctx stops the request or retry
// wait. Cancellation does not guarantee that the email was not accepted.
func (e *PendingEmail) Send(ctx context.Context) (*SendReceipt, error) {
	for attempt := 0; ; attempt++ {
		receipt, err := e.client.request(ctx, http.MethodPost, "/api/v1/emails", e.body, e.key)
		if err == nil {
			return receipt, nil
		}
		var apiError *APIError
		if attempt >= e.retries || !errors.As(err, &apiError) || !apiError.Retryable() {
			return nil, err
		}
		delay := time.Duration(250*(1<<attempt))*time.Millisecond + time.Duration(mathrand.Float64()*float64(100*time.Millisecond))
		if apiError.RetryAfter != nil {
			delay = *apiError.RetryAfter
		}
		if delay > 30*time.Second {
			return nil, err
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil, ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) request(ctx context.Context, method, path string, body []byte, key string) (*SendReceipt, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	request, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Authorization", "Bearer "+c.apiKey)
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Content-Type", "application/json")
	if key != "" {
		request.Header.Set("Idempotency-Key", key)
	}
	response, err := c.http.Do(request)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, &APIError{Code: "connection_error", cause: err}
	}
	defer response.Body.Close()
	const maxResponseBytes = 1 << 20
	data, readError := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		var payload struct {
			Code   string              `json:"code"`
			Errors map[string][]string `json:"errors"`
		}
		_ = json.Unmarshal(data, &payload)
		if payload.Code == "" {
			payload.Code = "request_error"
		}
		return nil, &APIError{
			Status: response.StatusCode, Code: payload.Code, Errors: payload.Errors,
			RetryAfter: parseRetryAfter(response.Header.Get("Retry-After"), time.Now()),
		}
	}
	if readError != nil || len(data) > maxResponseBytes {
		return nil, &APIError{Code: "invalid_response", cause: readError}
	}
	var receipt SendReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, &APIError{Code: "invalid_response", cause: err}
	}
	if receipt.ID == "" || receipt.Status == "" {
		return nil, &APIError{Code: "invalid_response"}
	}
	return &receipt, nil
}

func parseRetryAfter(value string, now time.Time) *time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	if seconds, err := strconv.ParseFloat(value, 64); err == nil {
		if math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 0 {
			return nil
		}
		// Saturate before converting to avoid overflow turning a long wait negative.
		delay := time.Duration(math.MaxInt64)
		if seconds < float64(math.MaxInt64)/float64(time.Second) {
			delay = time.Duration(seconds * float64(time.Second))
		}
		return &delay
	}
	if date, err := http.ParseTime(value); err == nil {
		delay := max(time.Duration(0), date.Sub(now))
		return &delay
	}
	return nil
}
