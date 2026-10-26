package service

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// BarkPushURL validates the server selection and returns the JSON push endpoint.
func BarkPushURL(config map[string]interface{}) (string, error) {
	key, _ := config["key"].(string)
	if strings.TrimSpace(key) == "" {
		return "", fmt.Errorf("Bark 配置缺少 key")
	}
	serverType, _ := config["serverType"].(string)
	serverURL := "https://api.day.app"
	switch serverType {
	case "", "official":
	case "custom":
		serverURL, _ = config["serverUrl"].(string)
	default:
		return "", fmt.Errorf("无效的 Bark 服务器类型")
	}
	u, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil || u.Hostname() == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", fmt.Errorf("Bark 服务器地址须为 HTTP 或 HTTPS 地址，且不含认证信息、查询参数或片段")
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/push"
	u.RawPath = ""
	return u.String(), nil
}

// SendBarkByConfig sends the existing formatted notification unchanged as body.
func (n *Notifier) SendBarkByConfig(ctx context.Context, config map[string]interface{}, message string) error {
	endpoint, err := BarkPushURL(config)
	if err != nil {
		return err
	}
	key, _ := config["key"].(string)
	data, err := json.Marshal(map[string]string{"device_key": strings.TrimSpace(key), "body": message})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json; charset=utf-8")
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("Bark 请求失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("Bark 请求失败，状态码: %d", resp.StatusCode)
	}
	var result struct {
		Code int `json:"code"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("Bark 返回无效响应")
	}
	if result.Code != 200 {
		return fmt.Errorf("Bark 拒绝了请求，错误码: %d", result.Code)
	}
	return nil
}
