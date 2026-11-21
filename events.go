package sendery

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"net/http"
	"net/url"
)

type EventObject struct {
	Type string `json:"type"`
	ID   string `json:"id"`
}
type EventInput struct {
	Name          string         `json:"name"`
	Object        EventObject    `json:"object"`
	CorrelationID string         `json:"correlation_id,omitempty"`
	Data          map[string]any `json:"data"`
}
type EventReceipt struct {
	ID              string       `json:"id"`
	Status          string       `json:"status"`
	Name            string       `json:"name,omitempty"`
	ReceivedAt      string       `json:"received_at,omitempty"`
	MatchedTriggers int          `json:"matched_triggers"`
	Environment     string       `json:"environment,omitempty"`
	Runs            []TriggerRun `json:"runs,omitempty"`
}
type TriggerRun struct {
	ID        string          `json:"id"`
	TriggerID int64           `json:"trigger_id"`
	Status    string          `json:"status"`
	DueAt     *string         `json:"due_at"`
	ErrorCode *string         `json:"error_code"`
	Actions   []TriggerAction `json:"actions"`
}
type TriggerAction struct {
	Position int     `json:"position"`
	Type     string  `json:"type"`
	Status   string  `json:"status"`
	EmailID  *string `json:"email_id"`
}

// PendingEvent freezes an event payload and preserves its key on retries.
type PendingEvent struct{ request *PendingEmail }

func (c *Client) PrepareEvent(input EventInput, options ...SendOption) (*PendingEvent, error) {
	if input.Data == nil {
		input.Data = map[string]any{}
	}
	body, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	request := &PendingEmail{client: c, body: body, key: rand.Text()}
	for _, option := range options {
		if err := option(request); err != nil {
			return nil, err
		}
	}
	return &PendingEvent{request: request}, nil
}
func (e *PendingEvent) IdempotencyKey() string { return e.request.key }
func (e *PendingEvent) Send(ctx context.Context) (*EventReceipt, error) {
	return sendPrepared[EventReceipt](ctx, e.request, "/api/v1/events")
}
func (c *Client) SendEvent(ctx context.Context, input EventInput, options ...SendOption) (*EventReceipt, error) {
	event, err := c.PrepareEvent(input, options...)
	if err != nil {
		return nil, err
	}
	return event.Send(ctx)
}
func (c *Client) GetEvent(ctx context.Context, id string) (*EventReceipt, error) {
	data, err := c.requestJSON(ctx, http.MethodGet, "/api/v1/events/"+url.PathEscape(id), nil, "")
	if err != nil {
		return nil, err
	}
	var receipt EventReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, &APIError{Code: "invalid_response", cause: err}
	}
	return &receipt, nil
}
func (c *Client) ForgetObject(ctx context.Context, object EventObject) (*EventReceipt, error) {
	body, err := json.Marshal(object)
	if err != nil {
		return nil, err
	}
	data, err := c.requestJSON(ctx, http.MethodDelete, "/api/v1/event-objects", body, "")
	if err != nil {
		return nil, err
	}
	var receipt EventReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, &APIError{Code: "invalid_response", cause: err}
	}
	return &receipt, nil
}

// EventField defines a readable field. Examples should contain synthetic data only.
type EventField struct {
	Label    string `json:"label"`
	Path     string `json:"path"`
	Type     string `json:"type"`
	Required bool   `json:"required,omitempty"`
	Example  any    `json:"example,omitempty"`
}
type EventRecipient struct {
	Label  string `json:"label"`
	Email  string `json:"email"`
	Name   string `json:"name,omitempty"`
	Locale string `json:"locale,omitempty"`
}
type EventRecordType struct {
	Type  string `json:"type"`
	Label string `json:"label"`
}
type EventDefinition struct {
	Label      string                    `json:"label"`
	Object     EventRecordType           `json:"object"`
	Fields     map[string]EventField     `json:"fields"`
	Recipients map[string]EventRecipient `json:"recipients"`
}
type EventTypeReceipt struct {
	Name     string `json:"name"`
	Verified bool   `json:"verified"`
}
type EventType struct {
	EventDefinition
	Name           string  `json:"name"`
	Verified       bool    `json:"verified"`
	LastReceivedAt *string `json:"last_received_at"`
}

func (c *Client) DefineEvent(ctx context.Context, name string, definition EventDefinition) (*EventTypeReceipt, error) {
	if definition.Fields == nil {
		definition.Fields = map[string]EventField{}
	}
	if definition.Recipients == nil {
		definition.Recipients = map[string]EventRecipient{}
	}
	body, err := json.Marshal(definition)
	if err != nil {
		return nil, err
	}
	data, err := c.requestJSON(ctx, http.MethodPut, "/api/v1/event-types/"+url.PathEscape(name), body, "")
	if err != nil {
		return nil, err
	}
	var receipt EventTypeReceipt
	if err := json.Unmarshal(data, &receipt); err != nil {
		return nil, &APIError{Code: "invalid_response", cause: err}
	}
	return &receipt, nil
}
func (c *Client) GetEventTypes(ctx context.Context) ([]EventType, error) {
	data, err := c.requestJSON(ctx, http.MethodGet, "/api/v1/event-types", nil, "")
	if err != nil {
		return nil, err
	}
	var catalog struct {
		Events []EventType `json:"events"`
	}
	if err := json.Unmarshal(data, &catalog); err != nil {
		return nil, &APIError{Code: "invalid_response", cause: err}
	}
	return catalog.Events, nil
}
