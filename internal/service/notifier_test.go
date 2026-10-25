package service

import (
	"context"
	"go.uber.org/zap"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestNotificationMessageIncludesDeviceAlias(t *testing.T) {
	for _, kind := range []string{"sms", "call", "flymode"} {
		msg := NotificationMessage{Type: kind, DeviceName: "办公室设备", From: "10086", Content: "测试内容", Timestamp: 1800000000}
		if text := msg.String(); !strings.Contains(text, "设备：办公室设备\n") || !strings.Contains(text, "10086") {
			t.Fatalf("%s notification missing device alias: %q", kind, text)
		}
	}
}

func TestNotificationHTTP200MayStillBeFailure(t *testing.T) {
	for _, tc := range []struct {
		body   string
		failed bool
	}{{`{"errcode":0}`, false}, {`{"errcode":310000}`, true}, {`{"code":19001}`, true}, {`{"ok":false}`, true}} {
		t.Run(tc.body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(tc.body))
			}))
			defer server.Close()
			_, err := NewNotifier(zap.NewNop()).sendJSONRequest(context.Background(), server.URL, map[string]string{"text": "test"})
			if (err != nil) != tc.failed {
				t.Fatalf("error=%v want failure=%v", err, tc.failed)
			}
		})
	}
}
