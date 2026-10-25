package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dushixiang/uart_sms_forwarder/internal/models"
	"github.com/dushixiang/uart_sms_forwarder/internal/service"
	"github.com/labstack/echo/v5"
)

func TestNotificationUsesCurrentDeviceCardAlias(t *testing.T) {
	m := testManager(t)
	addSerial(t, m, "A")
	addSerial(t, m, "B")
	ctx := context.Background()
	var received []map[string]string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]string
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		received = append(received, body)
		w.WriteHeader(http.StatusNoContent)
	}))
	defer server.Close()
	channel := models.NotificationChannelConfig{Type: "webhook", Enabled: true, Config: map[string]any{
		"url": server.URL, "contentType": "application/json",
		"body": `{"device_name":"{{device_name}}","content":"{{content}}"}`,
	}}
	for _, id := range []string{"A", "B"} {
		if err := m.SetSetting(ctx, id, service.PropertyIDNotificationChannels, "operator", mustJSON(t, []models.NotificationChannelConfig{channel})); err != nil {
			t.Fatal(err)
		}
	}
	e := echo.New()
	m.Register(e.Group("/api"))
	rename := func(id, name string) {
		t.Helper()
		req := httptest.NewRequest(http.MethodPut, "/api/devices/"+id, strings.NewReader(string(mustJSON(t, map[string]string{"name": name}))))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		if rec.Code != http.StatusNoContent {
			t.Fatal(rec.Code, rec.Body.String())
		}
	}
	name := "办公室\"一号\n设备"
	rename("A", name)
	rename("B", "仓库设备")
	job := notificationJob{Channel: channel, Message: service.NotificationMessage{Type: "sms", DeviceName: "旧名称", Content: "hello"}}
	if err := m.deliverNotification(ctx, "A", job); err != nil {
		t.Fatal(err)
	}
	rename("A", "办公室新别名")
	if err := m.deliverNotification(ctx, "A", job); err != nil {
		t.Fatal(err)
	}
	if err := m.deliverNotification(ctx, "B", job); err != nil {
		t.Fatal(err)
	}
	if err := m.TestNotification(ctx, "A", "webhook"); err != nil {
		t.Fatal(err)
	}
	for i, want := range []string{name, "办公室新别名", "仓库设备", "办公室新别名"} {
		if len(received) <= i || received[i]["device_name"] != want {
			t.Fatalf("notification %d did not use current alias", i)
		}
	}
	rename("A", "A")
	msg, err := m.deviceNotification(ctx, "A", job.Message)
	if err != nil || msg.DeviceName != "未命名设备" {
		t.Fatal(msg.DeviceName, err)
	}
}
