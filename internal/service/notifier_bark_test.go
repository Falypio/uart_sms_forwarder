package service

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"go.uber.org/zap"
)

func TestBarkPushURL(t *testing.T) {
	for _, tc := range []struct {
		config map[string]interface{}
		want   string
	}{
		{map[string]interface{}{"key": "abc"}, "https://api.day.app/push"},
		{map[string]interface{}{"key": "abc", "serverType": "official", "serverUrl": "https://ignored.invalid"}, "https://api.day.app/push"},
		{map[string]interface{}{"key": "abc", "serverType": "custom", "serverUrl": "https://example.com/bark/"}, "https://example.com/bark/push"},
		{map[string]interface{}{"key": " "}, ""},
		{map[string]interface{}{"key": "abc", "serverType": "custom"}, ""},
		{map[string]interface{}{"key": "abc", "serverType": "other"}, ""},
		{map[string]interface{}{"key": "abc", "serverType": "custom", "serverUrl": "ftp://example.com"}, ""},
		{map[string]interface{}{"key": "abc", "serverType": "custom", "serverUrl": "https://example.com?key=abc"}, ""},
	} {
		got, err := BarkPushURL(tc.config)
		if got != tc.want || (err != nil) != (tc.want == "") {
			t.Fatalf("got %q, %v; want %q", got, err, tc.want)
		}
	}
}

func TestBarkPreservesNotificationAndChecksResponse(t *testing.T) {
	for _, kind := range []string{"sms", "call", "flymode"} {
		for _, response := range []string{`{"code":200}`, `{"code":400}`, `{}`, `invalid`} {
			t.Run(kind+response, func(t *testing.T) {
				message := NotificationMessage{Type: kind, DeviceName: "测试设备", From: "10086", Content: "验证码：1234\n引号\" / & 😀", Timestamp: 1800000000}.String()
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.Method != http.MethodPost || r.URL.Path != "/prefix/push" || r.Header.Get("Content-Type") != "application/json; charset=utf-8" {
						t.Error("unexpected Bark request")
					}
					var payload map[string]string
					if err := json.NewDecoder(r.Body).Decode(&payload); err != nil || payload["body"] != message || payload["device_key"] != "test-key" || len(payload) != 2 {
						t.Errorf("unexpected payload: %v, %v", payload, err)
					}
					_, _ = w.Write([]byte(response))
				}))
				defer server.Close()
				err := NewNotifier(zap.NewNop()).SendBarkByConfig(context.Background(), map[string]interface{}{"serverType": "custom", "serverUrl": server.URL + "/prefix/", "key": "test-key"}, message)
				if (err == nil) != (response == `{"code":200}`) {
					t.Fatalf("unexpected result: %v", err)
				}
			})
		}
	}
}
