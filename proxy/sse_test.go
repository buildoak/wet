package proxy

import (
	"io"
	"strings"
	"testing"
)

func TestSSEInterceptorExtractsUsage(t *testing.T) {
	sseStream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"claude-sonnet-4-20250514","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":1523,"output_tokens":0,"cache_creation_input_tokens":100,"cache_read_input_tokens":50}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_delta
data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello world"}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":42}}

event: message_stop
data: {"type":"message_stop"}

`
	body := io.NopCloser(strings.NewReader(sseStream))
	interceptor := newSSEInterceptor(body)

	got, err := io.ReadAll(interceptor)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	_ = interceptor.Close()

	if string(got) != sseStream {
		t.Fatal("SSE stream was modified; bytes must pass through unchanged")
	}

	usage := interceptor.Usage()
	if usage.InputTokens != 1523 {
		t.Errorf("InputTokens = %d, want 1523", usage.InputTokens)
	}
	if usage.OutputTokens != 42 {
		t.Errorf("OutputTokens = %d, want 42", usage.OutputTokens)
	}
	if usage.CacheCreationInputTokens != 100 {
		t.Errorf("CacheCreationInputTokens = %d, want 100", usage.CacheCreationInputTokens)
	}
	if usage.CacheReadInputTokens != 50 {
		t.Errorf("CacheReadInputTokens = %d, want 50", usage.CacheReadInputTokens)
	}
}

func TestSSEInterceptorCumulativeDelta(t *testing.T) {
	sseStream := `event: message_start
data: {"type":"message_start","message":{"id":"msg_test","type":"message","role":"assistant","content":[],"model":"claude-opus-4-6","stop_reason":null,"stop_sequence":null,"usage":{"input_tokens":2679,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":3}}}

event: content_block_start
data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}

event: content_block_stop
data: {"type":"content_block_stop","index":0}

event: message_delta
data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"input_tokens":10682,"cache_creation_input_tokens":0,"cache_read_input_tokens":0,"output_tokens":510}}

event: message_stop
data: {"type":"message_stop"}

`
	body := io.NopCloser(strings.NewReader(sseStream))
	interceptor := newSSEInterceptor(body)
	_, _ = io.ReadAll(interceptor)
	_ = interceptor.Close()

	usage := interceptor.Usage()
	if usage.InputTokens != 10682 {
		t.Errorf("InputTokens = %d, want 10682", usage.InputTokens)
	}
	if usage.OutputTokens != 510 {
		t.Errorf("OutputTokens = %d, want 510", usage.OutputTokens)
	}
}

func TestSSEInterceptorNoUsage(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`{"type":"message","usage":{"input_tokens":500}}`))
	interceptor := newSSEInterceptor(body)
	_, _ = io.ReadAll(interceptor)
	_ = interceptor.Close()

	usage := interceptor.Usage()
	if usage.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0", usage.InputTokens)
	}
	if usage.OutputTokens != 0 {
		t.Errorf("OutputTokens = %d, want 0", usage.OutputTokens)
	}
}

func TestJSONUsageInterceptorExtractsUsage(t *testing.T) {
	jsonResp := `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"model":"claude-opus-4-6-20250514","stop_reason":"end_turn","usage":{"input_tokens":8,"output_tokens":1,"cache_creation_input_tokens":0,"cache_read_input_tokens":0}}`

	body := io.NopCloser(strings.NewReader(jsonResp))
	interceptor := newJSONUsageInterceptor(body)

	got, err := io.ReadAll(interceptor)
	if err != nil {
		t.Fatalf("Read failed: %v", err)
	}
	_ = interceptor.Close()

	if string(got) != jsonResp {
		t.Fatal("JSON response was modified; bytes must pass through unchanged")
	}

	usage := interceptor.Usage()
	if usage.InputTokens != 8 {
		t.Errorf("InputTokens = %d, want 8", usage.InputTokens)
	}
	if usage.OutputTokens != 1 {
		t.Errorf("OutputTokens = %d, want 1", usage.OutputTokens)
	}
}

func TestJSONUsageInterceptorWithCacheTokens(t *testing.T) {
	jsonResp := `{"id":"msg_test","type":"message","role":"assistant","content":[{"type":"text","text":"ok"}],"usage":{"input_tokens":3,"output_tokens":150,"cache_creation_input_tokens":33269,"cache_read_input_tokens":20760}}`

	body := io.NopCloser(strings.NewReader(jsonResp))
	interceptor := newJSONUsageInterceptor(body)
	_, _ = io.ReadAll(interceptor)
	_ = interceptor.Close()

	usage := interceptor.Usage()
	if usage.InputTokens != 3 {
		t.Errorf("InputTokens = %d, want 3", usage.InputTokens)
	}
	if usage.OutputTokens != 150 {
		t.Errorf("OutputTokens = %d, want 150", usage.OutputTokens)
	}
	if usage.CacheCreationInputTokens != 33269 {
		t.Errorf("CacheCreationInputTokens = %d, want 33269", usage.CacheCreationInputTokens)
	}
	if usage.CacheReadInputTokens != 20760 {
		t.Errorf("CacheReadInputTokens = %d, want 20760", usage.CacheReadInputTokens)
	}
}

func TestJSONUsageInterceptorInvalidJSON(t *testing.T) {
	body := io.NopCloser(strings.NewReader(`not json at all`))
	interceptor := newJSONUsageInterceptor(body)
	_, _ = io.ReadAll(interceptor)
	_ = interceptor.Close()

	usage := interceptor.Usage()
	if usage.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0", usage.InputTokens)
	}
}

func TestJSONUsageInterceptorEmptyBody(t *testing.T) {
	body := io.NopCloser(strings.NewReader(""))
	interceptor := newJSONUsageInterceptor(body)
	_, _ = io.ReadAll(interceptor)
	_ = interceptor.Close()

	usage := interceptor.Usage()
	if usage.InputTokens != 0 {
		t.Errorf("InputTokens = %d, want 0", usage.InputTokens)
	}
}
