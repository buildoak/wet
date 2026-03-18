package proxy

import (
	"bufio"
	"encoding/json"
	"io"
	"strings"
	"sync"
)

// UsageData holds token usage extracted from an SSE response stream.
type UsageData struct {
	InputTokens              int `json:"input_tokens"`
	OutputTokens             int `json:"output_tokens"`
	CacheCreationInputTokens int `json:"cache_creation_input_tokens,omitempty"`
	CacheReadInputTokens     int `json:"cache_read_input_tokens,omitempty"`
}

// sseInterceptor wraps a response body to extract usage data from SSE events
// while passing all bytes through unchanged.
type sseInterceptor struct {
	original io.ReadCloser
	pr       *io.PipeReader
	pw       *io.PipeWriter
	reader   io.Reader
	done     chan struct{}

	mu    sync.Mutex
	usage UsageData
}

// newSSEInterceptor wraps body so reads pass through unchanged while usage is
// parsed concurrently from SSE data lines.
func newSSEInterceptor(body io.ReadCloser) *sseInterceptor {
	pr, pw := io.Pipe()
	s := &sseInterceptor{
		original: body,
		pr:       pr,
		pw:       pw,
		reader:   io.TeeReader(body, pw),
		done:     make(chan struct{}),
	}
	go s.parse()
	return s
}

func (s *sseInterceptor) Read(p []byte) (int, error) {
	n, err := s.reader.Read(p)
	if err != nil {
		_ = s.pw.Close()
	}
	return n, err
}

func (s *sseInterceptor) Close() error {
	err := s.original.Close()
	_ = s.pw.Close()
	<-s.done
	return err
}

// Usage returns the extracted usage data.
func (s *sseInterceptor) Usage() UsageData {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.usage
}

func (s *sseInterceptor) parse() {
	defer close(s.done)

	scanner := bufio.NewScanner(s.pr)
	scanner.Buffer(make([]byte, 0, 64*1024), 512*1024)

	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		data := line[6:]

		if !strings.Contains(data, "message_start") && !strings.Contains(data, "message_delta") {
			continue
		}

		if strings.Contains(data, "message_start") {
			s.parseMessageStart(data)
			continue
		}
		if strings.Contains(data, "message_delta") {
			s.parseMessageDelta(data)
		}
	}

	_, _ = io.Copy(io.Discard, s.pr)
}

type messageStartEvent struct {
	Type    string `json:"type"`
	Message struct {
		Usage struct {
			InputTokens              int `json:"input_tokens"`
			OutputTokens             int `json:"output_tokens"`
			CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			CacheReadInputTokens     int `json:"cache_read_input_tokens"`
		} `json:"usage"`
	} `json:"message"`
}

type messageDeltaEvent struct {
	Type  string `json:"type"`
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func (s *sseInterceptor) parseMessageStart(data string) {
	var event messageStartEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return
	}
	if event.Type != "message_start" {
		return
	}

	s.mu.Lock()
	s.usage.InputTokens = event.Message.Usage.InputTokens
	s.usage.CacheCreationInputTokens = event.Message.Usage.CacheCreationInputTokens
	s.usage.CacheReadInputTokens = event.Message.Usage.CacheReadInputTokens
	s.mu.Unlock()
}

func (s *sseInterceptor) parseMessageDelta(data string) {
	var event messageDeltaEvent
	if err := json.Unmarshal([]byte(data), &event); err != nil {
		return
	}
	if event.Type != "message_delta" {
		return
	}

	s.mu.Lock()
	// message_delta usage is cumulative and supersedes message_start values.
	s.usage.OutputTokens = event.Usage.OutputTokens
	if event.Usage.InputTokens > 0 {
		s.usage.InputTokens = event.Usage.InputTokens
	}
	if event.Usage.CacheCreationInputTokens > 0 {
		s.usage.CacheCreationInputTokens = event.Usage.CacheCreationInputTokens
	}
	if event.Usage.CacheReadInputTokens > 0 {
		s.usage.CacheReadInputTokens = event.Usage.CacheReadInputTokens
	}
	s.mu.Unlock()
}

// jsonUsageInterceptor wraps a non-streaming JSON response body to extract
// usage data while passing bytes through unchanged. Claude Code ≥2.1.78
// sends non-streaming pre-flight requests (no "stream" field) that return
// application/json with a top-level "usage" object.
type jsonUsageInterceptor struct {
	original io.ReadCloser
	buf      []byte
	pos      int
	done     bool

	mu    sync.Mutex
	usage UsageData
}

func newJSONUsageInterceptor(body io.ReadCloser) *jsonUsageInterceptor {
	return &jsonUsageInterceptor{original: body}
}

func (j *jsonUsageInterceptor) Read(p []byte) (int, error) {
	// Buffer the entire response on first read, then serve from buffer.
	if !j.done {
		data, err := io.ReadAll(j.original)
		j.buf = data
		j.done = true
		j.parseUsage(data)
		if err != nil {
			return copy(p, j.buf[j.pos:]), err
		}
	}

	if j.pos >= len(j.buf) {
		return 0, io.EOF
	}
	n := copy(p, j.buf[j.pos:])
	j.pos += n
	if j.pos >= len(j.buf) {
		return n, io.EOF
	}
	return n, nil
}

func (j *jsonUsageInterceptor) Close() error {
	return j.original.Close()
}

func (j *jsonUsageInterceptor) Usage() UsageData {
	j.mu.Lock()
	defer j.mu.Unlock()
	return j.usage
}

// jsonMessageResponse matches the top-level structure of a non-streaming
// Messages API response. Only the usage field is needed.
type jsonMessageResponse struct {
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

func (j *jsonUsageInterceptor) parseUsage(data []byte) {
	var resp jsonMessageResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		return
	}
	j.mu.Lock()
	j.usage.InputTokens = resp.Usage.InputTokens
	j.usage.OutputTokens = resp.Usage.OutputTokens
	j.usage.CacheCreationInputTokens = resp.Usage.CacheCreationInputTokens
	j.usage.CacheReadInputTokens = resp.Usage.CacheReadInputTokens
	j.mu.Unlock()
}
