package util

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"github.com/xy/LogAgent/internal/config"
)

type DeepSeekMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type DeepSeekRequest struct {
	Model       string            `json:"model"`
	Messages    []DeepSeekMessage `json:"messages"`
	Temperature float64           `json:"temperature"`
	MaxTokens   int               `json:"max_tokens,omitempty"`
	Stop        []string          `json:"stop,omitempty"`
}

type DeepSeekResponse struct {
	Choices []struct {
		Message DeepSeekMessage `json:"message"`
	} `json:"choices"`
}

// CallDeepSeek 调用 DeepSeek API
func CallDeepSeek(messages []DeepSeekMessage) (string, error) {
	reqBody := DeepSeekRequest{
		Model:       "deepseek-chat",
		Messages:    messages,
		Temperature: 0.0,   // 强制最低温度，保证输出确定性
		MaxTokens:   4096,   // 确保诊断报告不会被截断（模板要求四章节输出）
		Stop:        []string{"\nObservation:", "Observation:"}, // 物理停止符，防止脑补
	}

	jsonData, _ := json.Marshal(reqBody)
	req, err := http.NewRequest("POST", config.GlobalConfig.DeepSeek.BaseURL+"/chat/completions", bytes.NewBuffer(jsonData))
	if err != nil {
		return "", err
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+config.GlobalConfig.DeepSeek.APIKey)

	client := &http.Client{}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	var dsResp DeepSeekResponse
	if err := json.NewDecoder(resp.Body).Decode(&dsResp); err != nil {
		return "", err
	}

	if len(dsResp.Choices) > 0 {
		return dsResp.Choices[0].Message.Content, nil
	}

	return "", fmt.Errorf("empty response from DeepSeek")
}
