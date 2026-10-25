package fleet

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/dushixiang/uart_sms_forwarder/internal/models"
	"github.com/labstack/echo/v5"
)

func TestConversationPhonePathEncoding(t *testing.T) {
	for _, peer := range []string{"+8615520480630", "10086", "%2B8615520480630"} {
		t.Run(peer, func(t *testing.T) {
			m := testManager(t)
			addSerial(t, m, "A")
			addSerial(t, m, "B")
			for _, msg := range []models.TextMessage{
				{ID: "incomingA", DeviceID: "A", From: peer, Type: models.MessageTypeIncoming},
				{ID: "outgoingA", DeviceID: "A", To: peer, Type: models.MessageTypeOutgoing},
				{ID: "incomingB", DeviceID: "B", From: peer, Type: models.MessageTypeIncoming},
				{ID: "otherA", DeviceID: "A", From: "15520480630", Type: models.MessageTypeIncoming},
			} {
				if err := m.DB.Create(&msg).Error; err != nil {
					t.Fatal(err)
				}
			}
			e := echo.New()
			m.Register(e.Group("/api"))
			request := func(method, path string) *httptest.ResponseRecorder {
				rec := httptest.NewRecorder()
				e.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
				return rec
			}
			path := "/api/devices/A/messages/conversations/" + url.PathEscape(peer)
			assertMessages := func(path string, count int) {
				t.Helper()
				rec := request(http.MethodGet, path+"/messages")
				var messages []models.TextMessage
				if rec.Code != http.StatusOK || json.Unmarshal(rec.Body.Bytes(), &messages) != nil || len(messages) != count {
					t.Fatalf("GET %s: status=%d body=%s, want %d messages", path, rec.Code, rec.Body.String(), count)
				}
			}
			assertMessages(path, 2)
			// Browser encodeURIComponent encodes '+'; also support a literal '+' path.
			encodedPath := "/api/devices/A/messages/conversations/" + url.QueryEscape(peer)
			assertMessages(encodedPath, 2)
			if rec := request(http.MethodDelete, encodedPath); rec.Code != http.StatusNoContent {
				t.Fatal(rec.Code, rec.Body.String())
			}
			assertMessages(encodedPath, 0)
			assertMessages("/api/devices/B/messages/conversations/"+url.QueryEscape(peer), 1)
			assertMessages("/api/devices/A/messages/conversations/15520480630", 1)
			var count int64
			if err := m.DB.Model(&models.TextMessage{}).Count(&count).Error; err != nil || count != 2 {
				t.Fatalf("unexpected remaining messages: %d, %v", count, err)
			}
		})
	}
}
