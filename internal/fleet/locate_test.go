package fleet

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/dushixiang/uart_sms_forwarder/internal/middleware"
	"github.com/dushixiang/uart_sms_forwarder/internal/util"
	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

func TestLocateControlIsScopedAndConfirmed(t *testing.T) {
	m := testManager(t)
	a, b := addSerial(t, m, "A"), addSerial(t, m, "B")
	ctx := context.Background()
	e := echo.New()
	api := e.Group("/api")
	api.Use(middleware.JWTMiddleware("test-secret", zap.NewNop()))
	m.Register(api)
	token, _, err := util.GenerateToken("operator", "test-secret", 1)
	if err != nil {
		t.Fatal(err)
	}
	request := func(body string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/api/devices/A/locate", strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	if rec := request(`{"enabled":true}`, false); rec.Code != http.StatusUnauthorized {
		t.Fatal(rec.Code)
	}
	for _, body := range []string{`{}`, `{"enabled":"true"}`, `{"enabled":1}`} {
		if rec := request(body, true); rec.Code != http.StatusBadRequest {
			t.Fatal(rec.Code, body)
		}
	}
	if len(a.messages) != 0 {
		t.Fatal("invalid request dispatched")
	}
	confirm := func(cmd Command, enabled bool, result string) {
		t.Helper()
		raw := mustJSON(t, map[string]any{"type": "cmd_response", "action": "set_locate", "device_id": "A", "request_id": cmd.ID, "result": result, "locating": enabled, "gpio": 21})
		if err := m.DB.Transaction(func(tx *gorm.DB) error { return m.eventTx(tx, "A", "", raw) }); err != nil {
			t.Fatal(err)
		}
	}
	locating := func(id string) bool {
		t.Helper()
		device, err := m.Device(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		var status map[string]any
		_ = json.Unmarshal([]byte(device.Status), &status)
		return status["locating"] == true
	}
	rec := request(`{"enabled":true}`, true)
	if rec.Code != http.StatusAccepted {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var start Command
	if err := json.Unmarshal(rec.Body.Bytes(), &start); err != nil {
		t.Fatal(err)
	}
	if start.Action != "set_locate" || start.Status != "pending" || len(a.messages) != 1 || len(b.messages) != 0 {
		t.Fatal("wrong command or target")
	}
	if a.messages[0]["enabled"] != true || a.messages[0]["device_id"] != "A" {
		t.Fatal(a.messages[0])
	}
	if locating("A") {
		t.Fatal("claimed success before device confirmation")
	}
	confirm(start, true, "ok")
	if !locating("A") || locating("B") {
		t.Fatal("locator state leaked or missing")
	}
	stop, err := m.Command(ctx, "A", "set_locate", map[string]any{"enabled": false}, "")
	if err != nil {
		t.Fatal(err)
	}
	confirm(stop, false, "error")
	if !locating("A") {
		t.Fatal("failed command changed confirmed state")
	}
	stop, err = m.Command(ctx, "A", "set_locate", map[string]any{"enabled": false}, "")
	if err != nil {
		t.Fatal(err)
	}
	confirm(stop, false, "ok")
	if locating("A") {
		t.Fatal("stop not reflected")
	}
	// A new status snapshot after a device reboot must clear any previous blink state.
	confirm(start, true, "ok") // Duplicate final result cannot reapply old state.
	if locating("A") {
		t.Fatal("old command result overwrote newer stop")
	}
	raw := mustJSON(t, map[string]any{"type": "heartbeat", "device_id": "A", "locating": true})
	if err := m.DB.Transaction(func(tx *gorm.DB) error { return m.eventTx(tx, "A", "", raw) }); err != nil {
		t.Fatal(err)
	}
	raw = mustJSON(t, map[string]any{"type": "system_ready", "device_id": "A", "locating": false})
	if err := m.DB.Transaction(func(tx *gorm.DB) error { return m.eventTx(tx, "A", "", raw) }); err != nil {
		t.Fatal(err)
	}
	if locating("A") {
		t.Fatal("reboot state not reset")
	}
	if err := m.DB.Model(&Device{}).Where("id = ?", "A").Update("online", false).Error; err != nil {
		t.Fatal(err)
	}
	if rec := request(`{"enabled":true}`, true); rec.Code != http.StatusBadRequest {
		t.Fatal("offline device accepted locate")
	}
}
