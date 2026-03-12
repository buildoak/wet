package messages

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
)

// Request is a partial parse of an Anthropic messages request.
// Unknown fields are preserved in Rest.
type Request struct {
	Messages []Message `json:"messages"`
	Rest     map[string]json.RawMessage
}

type Message struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or []ContentBlock
}

type ContentBlock struct {
	Type      string          `json:"type"`
	Text      string          `json:"text,omitempty"`
	ID        string          `json:"id,omitempty"`          // tool_use
	Name      string          `json:"name,omitempty"`        // tool_use
	Input     json.RawMessage `json:"input,omitempty"`       // tool_use
	ToolUseID string          `json:"tool_use_id,omitempty"` // tool_result
	Content   json.RawMessage `json:"content,omitempty"`     // tool_result: string or []ContentBlock
	IsError   bool            `json:"is_error,omitempty"`    // tool_result
}

// ParseRequest parses only the fields needed by wet and preserves all others.
func ParseRequest(body []byte) (*Request, error) {
	var envelope map[string]json.RawMessage
	if err := json.Unmarshal(body, &envelope); err != nil {
		return nil, err
	}

	req := &Request{
		Rest: make(map[string]json.RawMessage, len(envelope)),
	}

	for k, v := range envelope {
		req.Rest[k] = v
	}

	if rawMsgs, ok := envelope["messages"]; ok {
		if err := json.Unmarshal(rawMsgs, &req.Messages); err != nil {
			return nil, fmt.Errorf("parse messages: %w", err)
		}
		delete(req.Rest, "messages")
	}

	return req, nil
}

// Marshal reconstructs the request body with modified messages and preserved fields.
func (r *Request) Marshal() ([]byte, error) {
	if r == nil {
		return nil, errors.New("nil request")
	}

	out := make(map[string]json.RawMessage, len(r.Rest)+1)
	for k, v := range r.Rest {
		out[k] = v
	}

	rawMsgs, err := json.Marshal(r.Messages)
	if err != nil {
		return nil, err
	}
	out["messages"] = rawMsgs

	return json.Marshal(out)
}

// ParseContent parses Anthropic message content that can be either a string or block array.
func ParseContent(raw json.RawMessage) ([]ContentBlock, bool, error) {
	trim := bytes.TrimSpace(raw)
	if len(trim) == 0 || bytes.Equal(trim, []byte("null")) {
		return nil, false, nil
	}

	switch trim[0] {
	case '"':
		var s string
		if err := json.Unmarshal(trim, &s); err != nil {
			return nil, false, err
		}
		return []ContentBlock{{Type: "text", Text: s}}, true, nil
	case '[':
		var blocks []ContentBlock
		if err := json.Unmarshal(trim, &blocks); err != nil {
			return nil, false, err
		}
		return blocks, false, nil
	default:
		return nil, false, fmt.Errorf("unsupported content format: %s", string(trim))
	}
}
