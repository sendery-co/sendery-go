# Sendery for Go

Send transactional email from Go with Sendery templates.

[Documentation](https://sendery.co/en/docs/go) · [API reference](https://sendery.co/en/docs/send-email) · [Changelog](CHANGELOG.md)

## Requirements

Go 1.26+. The SDK uses only the Go standard library.

## Install

From a project with a `go.mod` file:

```bash
go get github.com/sendery-co/sendery-go@v0.1.1
```

## Set up

Choose a published template and create a [project API key](https://sendery.co/en/docs/authentication). Store the key as `SENDERY_API_KEY` on your server.

```bash
export SENDERY_API_KEY="your_project_api_key"
```

## Send an email

Replace `your-template` with your published template’s key and `Data` with its variables.

The response contains the accepted email’s `ID` and `Status`. Set `Locale` to choose a [template language](https://sendery.co/en/docs/languages). Omit `Data` when the template has no variables; it is sent as an empty JSON object.

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
		Template: "your-template",
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

## Send a specific version

Choose a [published template version](https://sendery.co/en/docs/send-email#section-5) to keep sending it after newer versions are published. By default, Sendery uses the latest version.

```go
email, err := client.Prepare(sendery.SendEmailInput{
	To:       "alex@example.com",
	Template: "your-template",
	Data: map[string]any{
		"name":       "Alex",
		"action_url": "https://example.com/start",
	},
}, sendery.WithIdempotencyKey("your-idempotency-key"))
if err != nil {
	log.Fatal(err)
}

pinned, err := email.Version(3)
if err != nil {
	log.Fatal(err)
}

receipt, err := pinned.Send(context.Background())
if err != nil {
	log.Fatal(err)
}
fmt.Println(receipt.ID)
```

## Attachments

Add files to `SendEmailInput.Attachments`. Pass the file bytes in `Content`; the SDK handles base64 encoding.

Send up to 10 files totaling 5 MB. See the [attachment reference](https://sendery.co/en/docs/send-email#section-6) for supported formats and limits.

```go
file, err := os.ReadFile("document.pdf")
if err != nil {
    log.Fatal(err)
}
receipt, err := client.Send(context.Background(), sendery.SendEmailInput{
    To:       "alex@example.com",
    Template: "your-template",
    Data: map[string]any{
        "name":       "Alex",
        "action_url": "https://example.com/start",
    },
    Attachments: []sendery.Attachment{{
        Filename:    "document.pdf",
        Content:     file,
        ContentType: "application/pdf",
    }},
}, sendery.WithIdempotencyKey("your-idempotency-key"))
if err != nil {
    log.Fatal(err)
}
fmt.Println(receipt.ID)
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

Use `WithRetries(3)` for up to three extra attempts after temporary failures. Keep the same [idempotency key and email data](https://sendery.co/en/docs/idempotency) on every attempt.

```go
email, err := client.Prepare(sendery.SendEmailInput{
	To:       "alex@example.com",
	Template: "your-template",
	Data: map[string]any{
		"name":       "Alex",
		"action_url": "https://example.com/start",
	},
}, sendery.WithIdempotencyKey("your-idempotency-key"), sendery.WithRetries(3))
if err != nil {
	log.Fatal(err)
}

receipt, err := email.Send(context.Background())
if err != nil {
	log.Fatal(err)
}
fmt.Println(receipt.ID)
```

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
