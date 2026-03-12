package messages

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestParseRequestSimpleStringContent(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hello"}]}`)
	req, err := ParseRequest(body)
	if err != nil {
		t.Fatalf("ParseRequest failed: %v", err)
	}

	if len(req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(req.Messages))
	}
	if req.Messages[0].Role != "user" {
		t.Fatalf("unexpected role: %s", req.Messages[0].Role)
	}

	blocks, isString, err := ParseContent(req.Messages[0].Content)
	if err != nil {
		t.Fatalf("ParseContent failed: %v", err)
	}
	if !isString {
		t.Fatal("expected string content")
	}
	if len(blocks) != 1 || blocks[0].Type != "text" || blocks[0].Text != "hello" {
		t.Fatalf("unexpected blocks: %+v", blocks)
	}
}

func TestParseRequestToolUseAndToolResult(t *testing.T) {
	body := []byte(`{
		"model":"claude-sonnet-4-20250514",
		"max_tokens":8096,
		"system":"sys",
		"messages":[
			{"role":"user","content":"fix the bug"},
			{"role":"assistant","content":[
				{"type":"text","text":"I'll look at the code."},
				{"type":"tool_use","id":"toolu_abc","name":"Bash","input":{"command":"git status"}}
			]},
			{"role":"user","content":[
				{"type":"tool_result","tool_use_id":"toolu_abc","content":"On branch main\n..."}
			]},
			{"role":"assistant","content":[{"type":"text","text":"I see the changes."}]},
			{"role":"user","content":"looks good, ship it"}
		]
	}`)

	req, err := ParseRequest(body)
	if err != nil {
		t.Fatalf("ParseRequest failed: %v", err)
	}
	if len(req.Messages) != 5 {
		t.Fatalf("expected 5 messages, got %d", len(req.Messages))
	}

	assistantBlocks, isString, err := ParseContent(req.Messages[1].Content)
	if err != nil {
		t.Fatalf("ParseContent assistant failed: %v", err)
	}
	if isString {
		t.Fatal("assistant content should be block array")
	}
	if len(assistantBlocks) != 2 {
		t.Fatalf("expected 2 assistant blocks, got %d", len(assistantBlocks))
	}
	if assistantBlocks[1].Type != "tool_use" || assistantBlocks[1].ID != "toolu_abc" || assistantBlocks[1].Name != "Bash" {
		t.Fatalf("unexpected tool_use block: %+v", assistantBlocks[1])
	}

	userBlocks, _, err := ParseContent(req.Messages[2].Content)
	if err != nil {
		t.Fatalf("ParseContent user failed: %v", err)
	}
	if len(userBlocks) != 1 || userBlocks[0].Type != "tool_result" || userBlocks[0].ToolUseID != "toolu_abc" {
		t.Fatalf("unexpected tool_result block: %+v", userBlocks)
	}
}

func TestParseRequestPreservesRestFields(t *testing.T) {
	body := []byte(`{
		"model":"claude-sonnet-4-20250514",
		"max_tokens":8096,
		"system":"sys",
		"messages":[{"role":"user","content":"hello"}]
	}`)

	req, err := ParseRequest(body)
	if err != nil {
		t.Fatalf("ParseRequest failed: %v", err)
	}

	if len(req.Rest) != 3 {
		t.Fatalf("expected 3 rest fields, got %d", len(req.Rest))
	}
	if _, ok := req.Rest["model"]; !ok {
		t.Fatal("expected model in Rest")
	}
	if _, ok := req.Rest["max_tokens"]; !ok {
		t.Fatal("expected max_tokens in Rest")
	}
	if _, ok := req.Rest["system"]; !ok {
		t.Fatal("expected system in Rest")
	}
}

func TestMarshalRoundTripEquivalentJSON(t *testing.T) {
	original := []byte(`{
		"model":"claude-sonnet-4-20250514",
		"max_tokens":8096,
		"system":"sys",
		"messages":[
			{"role":"user","content":"hello"},
			{"role":"assistant","content":[{"type":"text","text":"ok"}]}
		]
	}`)

	req, err := ParseRequest(original)
	if err != nil {
		t.Fatalf("ParseRequest failed: %v", err)
	}

	marshaled, err := req.Marshal()
	if err != nil {
		t.Fatalf("Marshal failed: %v", err)
	}

	var got any
	if err := json.Unmarshal(marshaled, &got); err != nil {
		t.Fatalf("unmarshal marshaled failed: %v", err)
	}
	var want any
	if err := json.Unmarshal(original, &want); err != nil {
		t.Fatalf("unmarshal original failed: %v", err)
	}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("round-trip mismatch\ngot: %#v\nwant: %#v", got, want)
	}
}
