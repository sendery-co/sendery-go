# Sendery for Go

Send transactional email from Go with Sendery templates.

[Documentation](https://sendery.co/en/docs/go) · [API reference](https://sendery.co/en/docs/send-email) · [Changelog](CHANGELOG.md)

## Requirements

Go 1.26+. The SDK uses only the Go standard library.

## Install

From a project with a `go.mod` file:

```bash
go get github.com/sendery-co/sendery-go@v0.1.0
```

## Set up

Publish a `welcome` template with `name` and `action_url` variables, and create a [project API key](https://sendery.co/en/docs/authentication). Store it as `SENDERY_API_KEY` on your server.

```bash
export SENDERY_API_KEY="your_project_api_key"
```

## Send an email

The response contains the accepted email's `ID` and `Status`. Set `Locale` to choose a [template language](https://sendery.co/en/docs/languages). Omit `Data` when the template has no variables; it is sent as an empty JSON object.

```go
package main

import (
	"context"
	"fmt"
	"log"
	"os"

	"github.com/sendery-co/sendery-go"
)

func main() {
	client, err := sendery.NewClient(os.Getenv("SENDERY_API_KEY"))
	if err != nil {
		log.Fatal(err)
	}

	receipt, err := client.Send(context.Background(), sendery.SendEmailInput{
		To:       "alex@example.com",
		Template: "welcome",
		Data: map[string]any{
			"name":       "Alex",
			"action_url": "https://example.com/start",
		},
	})
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(receipt.ID)
}
```

## Retrieve an email

Use the returned ID to [check delivery status](https://sendery.co/en/docs/get-email).

```go
message, err := client.Get(context.Background(), receipt.ID)
if err != nil {
	log.Fatal(err)
}
fmt.Println(message.Status)
```

## Retry a send

Use a unique key for each email and [keep the payload unchanged on retries](https://sendery.co/en/docs/idempotency). `Prepare` saves a copy of the payload. `WithRetries(3)` allows up to three additional attempts; the default is one attempt. You can also pass these options directly to `client.Send`.

```go
email, err := client.Prepare(sendery.SendEmailInput{
	To:       "alex@example.com",
	Template: "welcome",
	Data: map[string]any{
		"name":       "Alex",
		"action_url": "https://example.com/start",
	},
}, sendery.WithIdempotencyKey("welcome-123"), sendery.WithRetries(3))
if err != nil {
	log.Fatal(err)
}

receipt, err := email.Send(context.Background())
if err != nil {
	log.Fatal(err)
}
fmt.Println(receipt.ID)
```

When you omit `WithIdempotencyKey`, the SDK generates a key. Read it with `email.IdempotencyKey()` and reuse the prepared email to try the same send again. Each new `client.Send` call otherwise gets a new key.

`WithRetries` accepts `0` to `5`. Retries cover connection failures, unreadable successful responses, HTTP `500`, `502`, `503`, `504`, and `429` with code `rate_limited`. The SDK honors `Retry-After` up to 30 seconds; longer delays return the error so your application can retry later. Without that header, it uses increasing delays with jitter. Validation, billing, capacity, and idempotency conflicts are not retried.

## Handle errors

Use `errors.As` to inspect the [status, code, and field errors](https://sendery.co/en/docs/errors). `RetryAfter` is a `*time.Duration`, or `nil` when no delay is supplied. This example uses the prepared `email` above.

```go
// Add "errors" to your imports.
receipt, err := email.Send(context.Background())
if err != nil {
	var apiError *sendery.APIError
	if errors.As(err, &apiError) {
		log.Printf("%d: %s", apiError.Status, apiError.Code)
		for field, messages := range apiError.Errors {
			log.Printf("%s: %v", field, messages)
		}
		if apiError.RetryAfter != nil {
			log.Printf("Retry after %s", *apiError.RetryAfter)
		}
	}
	log.Fatal(err)
}
fmt.Println(receipt.ID)
```

## Set a deadline

Pass a context to `Send` or `Get` to cancel a request or limit its duration. The default HTTP timeout is 10 seconds per attempt. A context deadline also limits retry waits.

```go
// Add "time" to your imports. In an HTTP handler, use r.Context()
// as the parent context so requests stop when the caller disconnects.
ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
defer cancel()

receipt, err := email.Send(ctx)
if err != nil {
	log.Fatal(err)
}
fmt.Println(receipt.ID)
```

Cancellation returns `context.Canceled` or `context.DeadlineExceeded`. The email may already have been accepted, so reuse its key and payload if you try again.

## Configure the client

Use `WithHTTPClient` to supply an HTTP client with your timeout or transport:

```go
client, err := sendery.NewClient(os.Getenv("SENDERY_API_KEY"),
	sendery.WithHTTPClient(&http.Client{Timeout: 5 * time.Second}),
)
if err != nil {
	log.Fatal(err)
}
```

Add `net/http` and `time` to your imports for this example. The SDK copies the HTTP client, disables redirects, and uses a 10-second timeout if none is set. `WithBaseURL("http://localhost:8000")` can point to a local Sendery instance. Other hosts require HTTPS; omit `/api/v1` from the base URL.

## License

[MIT](LICENSE).
