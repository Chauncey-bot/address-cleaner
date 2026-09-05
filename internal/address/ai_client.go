package address

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// AIJudgeClient AI判定客户端接口
type AIJudgeClient interface {
	Judge(ctx context.Context, input, gsi string) (*AIJudge, error)
}

type aiAssistClient struct {
	baseURL string
	apiKey  string
	model   string
	client  *http.Client
}

// NewAIAssistClient 创建AI辅助复核客户端（OpenAI兼容接口）
func NewAIAssistClient(baseURL, apiKey, model string, timeoutMs int) AIJudgeClient {
	return &aiAssistClient{
		baseURL: strings.TrimRight(baseURL, "/"),
		apiKey:  apiKey,
		model:   model,
		client:  &http.Client{Timeout: time.Duration(timeoutMs) * time.Millisecond},
	}
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
}

func (c *aiAssistClient) Judge(ctx context.Context, input, gsi string) (*AIJudge, error) {
	prompt := fmt.Sprintf(`判断以下两个日本地址是否指向同一地点。
容错考虑：全角/半角数字、连字符、空白、行政区划表述（丁目/番地）差异；但门牌号明显不同则视为不同地址。

原始地址：%s
GSI地址：%s

只输出JSON，格式：{"same": true或false, "score": 0到100的整数, "reason": "简短理由"}`, input, gsi)

	body := chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: "你是地址一致性判定助手，只输出JSON，不要输出其他内容。"},
			{Role: "user", Content: prompt},
		},
		Temperature: 0,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/chat/completions", bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if c.apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+c.apiKey)
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("ai http %d", resp.StatusCode)
	}
	var cr chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return nil, err
	}
	if len(cr.Choices) == 0 {
		return nil, fmt.Errorf("ai empty choices")
	}
	content := extractJSON(cr.Choices[0].Message.Content)
	var out AIJudge
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return nil, err
	}
	out.Model = c.model
	return &out, nil
}

// extractJSON 从模型输出中截取JSON主体（容忍markdown代码块）
func extractJSON(s string) string {
	i := strings.Index(s, "{")
	j := strings.LastIndex(s, "}")
	if i >= 0 && j > i {
		return s[i : j+1]
	}
	return strings.TrimSpace(s)
}
