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
