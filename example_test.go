package sendery_test

import (
	"context"
	"errors"
	"fmt"
	"log"
	"os"
	"time"

	"github.com/sendery-co/sendery-go"
)

func ExampleClient_Send() {
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

func ExampleClient_Get() {
	client, _ := sendery.NewClient("project_api_key")
	receipt := &sendery.SendReceipt{ID: "msg_123"}

	message, err := client.Get(context.Background(), receipt.ID)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(message.Status)
}

func ExampleClient_Prepare() {
	client, _ := sendery.NewClient("project_api_key")

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
}

func ExampleAPIError() {
	client, _ := sendery.NewClient("project_api_key")
	email, _ := client.Prepare(sendery.SendEmailInput{To: "alex@example.com", Template: "welcome"})

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
}

func ExamplePendingEmail_Send() {
	client, _ := sendery.NewClient("project_api_key")
	email, _ := client.Prepare(sendery.SendEmailInput{To: "alex@example.com", Template: "welcome"})

	// Add "time" to your imports. In an HTTP handler, use r.Context()
	// as the parent context so requests stop when the caller disconnects.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	receipt, err := email.Send(ctx)
	if err != nil {
		log.Fatal(err)
	}
	fmt.Println(receipt.ID)
}
