package sendery

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return f(request)
}

func testClient(t *testing.T, transport roundTripFunc) *Client {
	t.Helper()
	client, err := NewClient("test-key", WithHTTPClient(&http.Client{Transport: transport}))
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func response(status int, body string) *http.Response {
	return &http.Response{StatusCode: status, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(body))}
}

func TestSendFreezesPayloadAndKeyAcrossRetriesAndRepeatedSends(t *testing.T) {
	var bodies, keys []string
	client := testClient(t, func(request *http.Request) (*http.Response, error) {
		if request.URL.String() != "https://sendery.co/api/v1/emails" || request.Method != "POST" {
			t.Errorf("unexpected request: %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Authorization") != "Bearer test-key" || request.Header.Get("Content-Type") != "application/json" || request.Header.Get("Accept") != "application/json" {
			t.Errorf("missing request headers")
		}
		body, _ := io.ReadAll(request.Body)
		bodies = append(bodies, string(body))
		keys = append(keys, request.Header.Get("Idempotency-Key"))
		if len(bodies) == 1 {
			retry := response(503, `{"code":"server_error"}`)
			retry.Header.Set("Retry-After", "0")
			return retry, nil
		}
		return response(202, `{"id":"msg_123","status":"queued","error_code":null}`), nil
	})
	items := []map[string]any{{"name": "Book", "quantity": 2}}
	input := SendEmailInput{To: "alex@example.com", Template: "receipt", Locale: "ja", Data: map[string]any{"name": "Alex", "items": items}}
	email, err := client.Prepare(input, WithRetries(3))
	if err != nil {
		t.Fatal(err)
	}
	input.Data["name"] = "Changed"
	items[0]["name"] = "Changed"
	for range 2 {
		receipt, err := email.Send(context.Background())
		if err != nil || receipt.ID != "msg_123" || receipt.Status != "queued" || receipt.ErrorCode != nil {
			t.Fatalf("unexpected receipt: %+v, %v", receipt, err)
		}
	}
	if len(bodies) != 3 || bodies[0] != bodies[1] || bodies[1] != bodies[2] {
		t.Fatalf("payload changed: %v", bodies)
	}
	if strings.Contains(bodies[0], "Changed") || !strings.Contains(bodies[0], `"locale":"ja"`) || !strings.Contains(bodies[0], `"quantity":2`) {
		t.Fatalf("incorrect snapshot: %s", bodies[0])
	}
	for _, key := range keys {
		if key != email.IdempotencyKey() || !idempotencyKeyPattern.MatchString(key) {
			t.Fatalf("invalid or changed key: %q", key)
		}
	}
}

func TestSendDefaultsAndCustomKey(t *testing.T) {
	var keys []string
	client := testClient(t, func(request *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(request.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(body["data"], map[string]any{}) {
			t.Errorf("nil data must become an object: %#v", body)
		}
		if _, present := body["locale"]; present {
			t.Error("empty locale must be omitted")
		}
		keys = append(keys, request.Header.Get("Idempotency-Key"))
		return response(202, `{"id":"msg","status":"queued"}`), nil
	})
	for range 2 {
		if _, err := client.Send(context.Background(), SendEmailInput{}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := client.Send(context.Background(), SendEmailInput{}, WithIdempotencyKey("welcome-123")); err != nil {
		t.Fatal(err)
	}
	if keys[0] == keys[1] || keys[2] != "welcome-123" {
		t.Fatalf("unexpected keys: %v", keys)
	}
}

func TestGetEscapesIDAndOmitsIdempotencyHeader(t *testing.T) {
	client := testClient(t, func(request *http.Request) (*http.Response, error) {
		if request.Method != "GET" || request.URL.EscapedPath() != "/api/v1/emails/a%2Fb%20%3F%23" || request.URL.RawQuery != "" {
			t.Errorf("incorrect retrieval URL: %s %s", request.Method, request.URL)
		}
		if request.Header.Get("Idempotency-Key") != "" {
			t.Error("GET must not have an idempotency key")
		}
		return response(200, `{"id":"msg","status":"delivered","created_at":"2026-09-28T01:00:00Z","submitted_at":"2026-09-28T01:00:01Z"}`), nil
	})
	receipt, err := client.Get(context.Background(), "a/b ?#")
	if err != nil || receipt.Status != "delivered" || receipt.SubmittedAt == nil || receipt.CreatedAt == "" {
		t.Fatalf("unexpected receipt: %+v, %v", receipt, err)
	}
	if _, err := client.Get(context.Background(), ""); err == nil {
		t.Fatal("accepted an empty ID")
	}
}

func TestRetriesAreBoundedAndOnlyForTemporaryFailures(t *testing.T) {
	for _, test := range []struct {
		name   string
		status int
		code   string
		retry  bool
	}{
		{"network", 0, "connection_error", true},
		{"invalid response", 200, "invalid_response", true},
		{"server", 500, "server_error", true},
		{"gateway", 502, "server_error", true},
		{"unavailable", 503, "server_error", true},
		{"timeout", 504, "server_error", true},
		{"rate limit", 429, "rate_limited", true},
		{"capacity", 429, "email_capacity_exceeded", false},
		{"validation", 422, "validation_error", false},
		{"authentication", 401, "invalid_api_key", false},
		{"billing", 402, "payment_required", false},
		{"conflict", 409, "idempotency_conflict", false},
		{"redirect", 302, "request_error", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			client := testClient(t, func(*http.Request) (*http.Response, error) {
				calls++
				if test.status == 0 {
					return nil, io.ErrUnexpectedEOF
				}
				result := response(test.status, `{"code":"`+test.code+`"}`)
				result.Header.Set("Retry-After", "0")
				return result, nil
			})
			_, err := client.Send(context.Background(), SendEmailInput{}, WithRetries(1))
			var apiError *APIError
			if !errors.As(err, &apiError) || apiError.Code != test.code || apiError.Retryable() != test.retry {
				t.Fatalf("unexpected error: %v", err)
			}
			want := 1
			if test.retry {
				want++
			}
			if calls != want {
				t.Fatalf("got %d attempts, want %d", calls, want)
			}
		})
	}
}

func TestNoRetriesByDefaultAndLongDelaysReturnImmediately(t *testing.T) {
	for _, delay := range []string{"0", "31", "999999999999999999999"} {
		calls := 0
		client := testClient(t, func(*http.Request) (*http.Response, error) {
			calls++
			result := response(429, `{"code":"rate_limited"}`)
			result.Header.Set("Retry-After", delay)
			return result, nil
		})
		var options []SendOption
		if delay != "0" {
			options = append(options, WithRetries(3))
		}
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		_, err := client.Send(ctx, SendEmailInput{}, options...)
		cancel()
		var apiError *APIError
		if calls != 1 || !errors.As(err, &apiError) || apiError.Code != "rate_limited" {
			t.Fatalf("delay %s: %d calls, %v", delay, calls, err)
		}
	}
}

func TestValidationErrorsExposeFields(t *testing.T) {
	client := testClient(t, func(*http.Request) (*http.Response, error) {
		return response(422, `{"code":"validation_error","errors":{"to":["Invalid email address."]}}`), nil
	})
	_, err := client.Send(context.Background(), SendEmailInput{})
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Status != 422 || len(apiError.Errors["to"]) != 1 || apiError.RetryAfter != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestMalformedResponses(t *testing.T) {
	for _, body := range []string{"not json", "null", "[]", `{}`, `{"id":12,"status":"queued"}`, `{"id":"msg"}`, `{"id":"msg","status":"queued"} trailing`, strings.Repeat("x", (1<<20)+1)} {
		client := testClient(t, func(*http.Request) (*http.Response, error) { return response(200, body), nil })
		_, err := client.Get(context.Background(), "msg")
		var apiError *APIError
		if !errors.As(err, &apiError) || apiError.Code != "invalid_response" || apiError.Status != 0 {
			t.Fatalf("unexpected error: %v", err)
		}
	}
	client := testClient(t, func(*http.Request) (*http.Response, error) { return response(502, "Bad Gateway"), nil })
	_, err := client.Get(context.Background(), "msg")
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Status != 502 || apiError.Code != "request_error" {
		t.Fatalf("lost HTTP status: %v", err)
	}
}

func TestCancellationStopsRequestsAndRetryWaits(t *testing.T) {
	for _, duringRequest := range []bool{true, false} {
		calls := 0
		client := testClient(t, func(request *http.Request) (*http.Response, error) {
			calls++
			if duringRequest {
				<-request.Context().Done()
				return nil, request.Context().Err()
			}
			result := response(503, `{"code":"server_error"}`)
			result.Header.Set("Retry-After", "30")
			return result, nil
		})
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		_, err := client.Send(ctx, SendEmailInput{}, WithRetries(5))
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || calls != 1 {
			t.Fatalf("cancellation did not stop sending: %d calls, %v", calls, err)
		}
		ctx, cancel = context.WithCancel(context.Background())
		cancel()
		_, err = client.Send(ctx, SendEmailInput{}, WithRetries(5))
		if !errors.Is(err, context.Canceled) || calls != 1 {
			t.Fatalf("sent a canceled request: %d calls, %v", calls, err)
		}
	}
}

func TestHTTPTimeoutIsReportedAsConnectionError(t *testing.T) {
	client, err := NewClient("key", WithHTTPClient(&http.Client{
		Timeout: 10 * time.Millisecond,
		Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			<-request.Context().Done()
			return nil, request.Context().Err()
		}),
	}))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Get(context.Background(), "msg")
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Status != 0 || apiError.Code != "connection_error" || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("unexpected timeout error: %v", err)
	}
}

func TestRedirectsAreNotFollowedAndCustomClientIsUnchanged(t *testing.T) {
	var redirected bool
	target := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { redirected = true }))
	defer target.Close()
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer origin.Close()
	custom := &http.Client{}
	client, err := NewClient("secret", WithBaseURL(origin.URL), WithHTTPClient(custom))
	if err != nil {
		t.Fatal(err)
	}
	_, err = client.Send(context.Background(), SendEmailInput{}, WithRetries(3))
	var apiError *APIError
	if !errors.As(err, &apiError) || apiError.Status != 307 || redirected {
		t.Fatalf("redirect must be returned without forwarding credentials: %v", err)
	}
	if custom.CheckRedirect != nil || custom.Timeout != 0 {
		t.Fatal("modified the caller's HTTP client")
	}
}

func TestInvalidConfigurationAndPayloads(t *testing.T) {
	for _, baseURL := range []string{"", "/relative", "http://example.com", "ftp://localhost", "https://user:password@example.com", "https://example.com?token=secret", "https://example.com?", "https://example.com#fragment", "://"} {
		if _, err := NewClient("key", WithBaseURL(baseURL)); err == nil {
			t.Errorf("accepted invalid base URL %q", baseURL)
		}
	}
	for _, baseURL := range []string{"https://example.com", "http://localhost:1234/", "http://127.0.0.1:1234", "http://[::1]:1234"} {
		if _, err := NewClient("key", WithBaseURL(baseURL)); err != nil {
			t.Errorf("rejected valid base URL %q: %v", baseURL, err)
		}
	}
	for _, key := range []string{"", "  ", "key\r\nInjected: header"} {
		if _, err := NewClient(key); err == nil {
			t.Error("accepted an invalid API key")
		}
	}
	for _, httpClient := range []*http.Client{nil, {Timeout: -time.Second}} {
		if _, err := NewClient("key", WithHTTPClient(httpClient)); err == nil {
			t.Error("accepted an invalid HTTP client")
		}
	}
	client, _ := NewClient("key")
	for _, key := range []string{"", "with space", "\r\n", strings.Repeat("x", 129)} {
		if _, err := client.Prepare(SendEmailInput{}, WithIdempotencyKey(key)); err == nil {
			t.Errorf("accepted invalid idempotency key %q", key)
		}
	}
	for _, retries := range []int{-1, 6} {
		if _, err := client.Prepare(SendEmailInput{}, WithRetries(retries)); err == nil {
			t.Errorf("accepted %d retries", retries)
		}
	}
	for _, value := range []any{math.NaN(), math.Inf(1), make(chan string)} {
		if _, err := client.Prepare(SendEmailInput{Data: map[string]any{"bad": value}}); err == nil {
			t.Error("accepted data that cannot be encoded as JSON")
		}
	}
}

func TestRetryAfter(t *testing.T) {
	now := time.Date(2026, 9, 28, 0, 0, 0, 0, time.UTC)
	for header, want := range map[string]time.Duration{"0": 0, "1.5": 1500 * time.Millisecond, now.Add(5 * time.Second).Format(http.TimeFormat): 5 * time.Second, now.Add(-time.Second).Format(http.TimeFormat): 0} {
		if got := parseRetryAfter(header, now); got == nil || *got != want {
			t.Errorf("%q: got %v, want %v", header, got, want)
		}
	}
	for _, header := range []string{"", "invalid", "NaN", "Inf", "-1"} {
		if got := parseRetryAfter(header, now); got != nil {
			t.Errorf("accepted invalid Retry-After %q", header)
		}
	}
}

func TestPreparedEmailCanBeSharedByGoroutines(t *testing.T) {
	client := testClient(t, func(*http.Request) (*http.Response, error) {
		return response(202, `{"id":"msg","status":"queued"}`), nil
	})
	email, _ := client.Prepare(SendEmailInput{})
	var group sync.WaitGroup
	for range 10 {
		group.Add(1)
		go func() {
			defer group.Done()
			if _, err := email.Send(context.Background()); err != nil {
				t.Error(err)
			}
		}()
	}
	group.Wait()
}
