package fleet

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"go.uber.org/zap"
)

func TestMQTTFirmwareAADCompatibilityPersistsAcrossRestart(t *testing.T) {
	for _, mode := range []string{"", luatFixedAADMode} {
		t.Run("aad-"+mode, func(t *testing.T) {
			m := testManager(t)
			token, err := m.Provision(context.Background(), "deviceA")
			if err != nil {
				t.Fatal(err)
			}
			var replies [][]byte
			m.mqtt = &mqttTransport{publish: func(_ string, b []byte) error { replies = append(replies, append([]byte(nil), b...)); return nil }}
			s := newTestSession(t, m, "deviceA")
			s.aad, _ = packetAAD(mode)
			registration := s.packet(t, "register", map[string]any{"registration_token": token, "key_id": "server-key-1", "features": map[string]any{"device_id": "deviceA"}})
			var damaged Envelope
			_ = json.Unmarshal(registration, &damaged)
			damaged.Tag = strings.Repeat("00", 16)
			if m.HandleMQTT("sms/v1/devices/deviceA/register", mustJSON(t, damaged)) == nil {
				t.Fatal("accepted bad authentication tag")
			}
			if len(replies) != 0 {
				t.Fatal("published before authentication")
			}
			if err := m.HandleMQTT("sms/v1/devices/deviceA/register", registration); err != nil {
				t.Fatal(err)
			}
			if got := s.down(t, replies[len(replies)-1]); got.Kind != "register_result" {
				t.Fatal(got.Kind)
			}
			var stored Session
			if err := m.DB.First(&stored, "id = ?", s.sid).Error; err != nil {
				t.Fatal(err)
			}
			if stored.AADMode != mode {
				t.Fatal("AAD mode not persisted")
			}
			if err := m.HandleMQTT("sms/v1/devices/deviceA/register", registration); err != nil {
				t.Fatal("registration retransmission:", err)
			}
			restarted, err := New(m.DB, m.Config, zap.NewNop())
			if err != nil {
				t.Fatal(err)
			}
			restarted.mqtt = m.mqtt
			event := map[string]any{"message_id": "status1", "data": map[string]any{"type": "heartbeat", "device_id": "deviceA", "locating": false}}
			// An established session must not switch AAD mode on individual messages.
			savedAAD := s.aad
			if mode == "" {
				s.aad = []byte("1234567890123456")
			} else {
				s.aad = nil
			}
			if restarted.HandleMQTT("sms/v1/devices/deviceA/status", s.packet(t, "event", event)) == nil {
				t.Fatal("session accepted a different AAD mode")
			}
			s.aad = savedAAD
			if err := restarted.HandleMQTT("sms/v1/devices/deviceA/status", s.packet(t, "event", event)); err != nil {
				t.Fatal(err)
			}
			if got := s.down(t, replies[len(replies)-1]); got.Kind != "ack" {
				t.Fatal(got.Kind)
			}
			if _, err := restarted.Command(context.Background(), "deviceA", "set_locate", map[string]any{"enabled": true}, ""); err != nil {
				t.Fatal(err)
			}
			got := s.down(t, replies[len(replies)-1])
			var command map[string]any
			if err := json.Unmarshal(got.Body, &command); err != nil {
				t.Fatal(err)
			}
			if got.Kind != "command" || command["action"] != "set_locate" {
				t.Fatal("wrong downlink command")
			}
		})
	}
}
