// Package llm 提供 OpenAI 兼容协议（/chat/completions）的最小可用客户端。
// 兼容 DeepSeek、通义、智谱、vLLM、Ollama、LM Studio 等服务端。
package llm

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"time"
)

// Message 是一条对话消息。
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ResponseFormat 用于约束输出格式，Type 取值 text 或 json_object。
type ResponseFormat struct {
	Type string `json:"type"`
}

// ChatRequest 是聊天补全请求体。
type ChatRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Temperature    *float64        `json:"temperature,omitempty"`
	MaxTokens      int             `json:"max_tokens,omitempty"`
	Stream         bool            `json:"stream,omitempty"`
	ResponseFormat *ResponseFormat `json:"response_format,omitempty"`
}

// Usage 是 token 用量统计。
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// Choice 是单个候选结果。
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	Delta        Message `json:"delta"`
	FinishReason string  `json:"finish_reason"`
}

// ChatResponse 是聊天补全响应体，流式场景下 Delta 有值。
type ChatResponse struct {
	ID      string   `json:"id"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   Usage    `json:"usage"`
}

// APIError 表示服务端返回的非 2xx 响应。
type APIError struct {
	StatusCode int
	Body       string
}

func (e *APIError) Error() string {
	body := e.Body
	if len(body) > 512 {
		body = body[:512] + "..."
	}
	return fmt.Sprintf("llm api error: status=%d body=%s", e.StatusCode, body)
}

// Config 是客户端配置。
type Config struct {
	BaseURL    string
	APIKey     string
	Model      string
	Timeout    time.Duration
	MaxRetries int
}

// Client 是 LLM 客户端。
type Client struct {
	cfg  Config
	http *http.Client
}

// New 创建客户端，超时为 0 时使用默认值 120s。
func New(cfg Config) *Client {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 120 * time.Second
	}
	if cfg.MaxRetries < 0 {
		cfg.MaxRetries = 0
	}
	return &Client{
		cfg:  cfg,
		http: &http.Client{Timeout: cfg.Timeout},
	}
}

func (c *Client) endpoint() string {
	return strings.TrimRight(c.cfg.BaseURL, "/") + "/chat/completions"
}

// do 发起请求，对网络错误与 429/5xx 做指数退避重试。
func (c *Client) do(ctx context.Context, req *ChatRequest) (*http.Response, error) {
	if c.cfg.BaseURL == "" {
		return nil, errors.New("base url is empty")
	}
	if c.cfg.APIKey == "" {
		return nil, errors.New("api key is empty")
	}

	payload, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	var lastErr error
	for attempt := 0; attempt <= c.cfg.MaxRetries; attempt++ {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint(), bytes.NewReader(payload))
		if err != nil {
			return nil, fmt.Errorf("build request: %w", err)
		}
		httpReq.Header.Set("Content-Type", "application/json")
		httpReq.Header.Set("Accept", "application/json")
		httpReq.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)

		resp, err := c.http.Do(httpReq)
		if err != nil {
			lastErr = err
			if !sleepBackoff(ctx, attempt) {
				return nil, err
			}
			continue
		}

		status := resp.StatusCode
		if status == http.StatusTooManyRequests || status >= 500 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
			resp.Body.Close()
			lastErr = &APIError{StatusCode: status, Body: string(body)}
			if attempt == c.cfg.MaxRetries || !sleepBackoff(ctx, attempt) {
				return nil, lastErr
			}
			continue
		}
		if status >= 400 {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8*1024))
			resp.Body.Close()
			return nil, &APIError{StatusCode: status, Body: string(body)}
		}
		return resp, nil
	}

	if lastErr == nil {
		lastErr = errors.New("request failed")
	}
	return nil, lastErr
}

func sleepBackoff(ctx context.Context, attempt int) bool {
	d := time.Duration(500*math.Pow(2, float64(attempt))) * time.Millisecond
	if d > 8*time.Second {
		d = 8 * time.Second
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// Chat 执行一次非流式补全。
func (c *Client) Chat(ctx context.Context, req *ChatRequest) (*ChatResponse, error) {
	if req.Model == "" {
		req.Model = c.cfg.Model
	}
	req.Stream = false
	resp, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var out ChatResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, errors.New("llm returned no choices")
	}
	if out.Model == "" {
		out.Model = req.Model
	}
	return &out, nil
}

// ChatStream 执行流式补全，onDelta 逐块接收增量文本。
// 返回的 Usage 在服务端未回传时为各字段 0。
func (c *Client) ChatStream(ctx context.Context, req *ChatRequest, onDelta func(string) error) (*Usage, error) {
	if req.Model == "" {
		req.Model = c.cfg.Model
	}
	req.Stream = true
	resp, err := c.do(ctx, req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var usage Usage
	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for scanner.Scan() {
		if err := ctx.Err(); err != nil {
			return &usage, err
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" || !strings.HasPrefix(line, "data:") {
			continue
		}
		data := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if data == "" {
			continue
		}
		if data == "[DONE]" {
			break
		}
		var chunk ChatResponse
		if err := json.Unmarshal([]byte(data), &chunk); err != nil {
			continue
		}
		if chunk.Usage.TotalTokens > 0 {
			usage = chunk.Usage
		}
		if len(chunk.Choices) > 0 && chunk.Choices[0].Delta.Content != "" {
			if err := onDelta(chunk.Choices[0].Delta.Content); err != nil {
				return &usage, err
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return &usage, err
	}
	return &usage, nil
}
