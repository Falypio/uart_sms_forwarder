package fleet

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/rsa"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dushixiang/uart_sms_forwarder/config"
	"github.com/dushixiang/uart_sms_forwarder/internal/middleware"
	"github.com/dushixiang/uart_sms_forwarder/internal/models"
	"github.com/dushixiang/uart_sms_forwarder/internal/service"
	"github.com/dushixiang/uart_sms_forwarder/internal/util"
	"github.com/glebarez/sqlite"
	"github.com/labstack/echo/v5"
	"go.uber.org/zap"
	"go.yaml.in/yaml/v3"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func testManager(t *testing.T) *Manager {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(filepath.Join(t.TempDir(), "test.db")), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { _ = sqlDB.Close() })
	m, err := New(db, config.AppConfig{Serial: config.SerialConfig{Disabled: true}, MQTT: config.MQTTConfig{PrivateKeyFile: filepath.Join(t.TempDir(), "key.pem")}}, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func mustJSON(t *testing.T, v any) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

type captured struct {
	mu       sync.Mutex
	messages []map[string]any
}

func (c *captured) Send(_ context.Context, b map[string]any) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.messages = append(c.messages, b)
	return nil
}
func (c *captured) Close() {}

func addSerial(t *testing.T, m *Manager, id string) *captured {
	t.Helper()
	d := Device{ID: id, Name: id, AccessMode: "serial", Online: true, LastSeen: time.Now().UnixMilli(), LastSMSAt: time.Now().UnixMilli()}
	if err := m.DB.Create(&d).Error; err != nil {
		t.Fatal(err)
	}
	c := &captured{}
	if err := m.attach(id, "serial", c); err != nil {
		t.Fatal(err)
	}
	return c
}

type testSession struct {
	aad              []byte
	root             []byte
	sid, id, wrapped string
	seq              uint64
}

func newTestSession(t *testing.T, m *Manager, id string) *testSession {
	t.Helper()
	root := make([]byte, 32)
	_, _ = rand.Read(root)
	sid := make([]byte, 16)
	_, _ = rand.Read(sid)
	wrapped, err := rsa.EncryptPKCS1v15(rand.Reader, &m.private.PublicKey, root)
	if err != nil {
		t.Fatal(err)
	}
	return &testSession{root: root, sid: hex.EncodeToString(sid), id: id, wrapped: hex.EncodeToString(wrapped)}
}
func (s *testSession) packet(t *testing.T, kind string, body any) []byte {
	t.Helper()
	s.seq++
	b := mustJSON(t, body)
	clear := mustJSON(t, innerPacket{V: 1, DeviceID: s.id, SID: s.sid, Direction: "up", Seq: s.seq, Kind: kind, Body: b})
	aead, err := gcm(s.root, s.sid, "up")
	if err != nil {
		t.Fatal(err)
	}
	iv := nonce("up", s.seq)
	ct := aead.Seal(nil, iv, clear, s.aad)
	p := Envelope{V: 1, DeviceID: s.id, SID: s.sid, Direction: "up", Seq: s.seq, Kind: kind, IV: hex.EncodeToString(iv), Ciphertext: hex.EncodeToString(ct[:len(ct)-16]), Tag: hex.EncodeToString(ct[len(ct)-16:])}
	if kind == "register" {
		p.KeyID = "server-key-1"
		p.WrappedKey = s.wrapped
	}
	return mustJSON(t, p)
}
func (s *testSession) down(t *testing.T, b []byte) innerPacket {
	t.Helper()
	var p Envelope
	if json.Unmarshal(b, &p) != nil {
		t.Fatal("bad response")
	}
	aead, err := gcm(s.root, s.sid, "down")
	if err != nil {
		t.Fatal(err)
	}
	ct, _ := hex.DecodeString(p.Ciphertext + p.Tag)
	clear, err := aead.Open(nil, nonce("down", p.Seq), ct, s.aad)
	if err != nil {
		t.Fatal(err)
	}
	var inner innerPacket
	if json.Unmarshal(clear, &inner) != nil {
		t.Fatal("bad decrypted response")
	}
	return inner
}

func TestMQTTAuthenticationReplayDedupAndRestart(t *testing.T) {
	m := testManager(t)
	ctx := context.Background()
	token, err := m.Provision(ctx, "deviceA")
	if err != nil {
		t.Fatal(err)
	}
	var replies [][]byte
	m.mqtt = &mqttTransport{publish: func(_ string, b []byte) error { replies = append(replies, append([]byte(nil), b...)); return nil }}
	s := newTestSession(t, m, "deviceA")
	request := map[string]any{"registration_token": token, "key_id": "server-key-1", "features": map[string]any{"device_id": "deviceA", "name": "A"}}
	registration := s.packet(t, "register", request)
	if err := m.HandleMQTT("sms/v1/devices/deviceA/register", registration); err != nil {
		t.Fatal(err)
	}
	if got := s.down(t, replies[0]); got.Kind != "register_result" {
		t.Fatal(got.Kind)
	}
	data := map[string]any{"device_id": "deviceA", "type": "incoming_sms", "from": "10086", "content": "only A"}
	msg := map[string]any{"message_id": "message1", "data": data}
	event := s.packet(t, "event", msg)
	if err := m.HandleMQTT("sms/v1/devices/deviceA/events", event); err != nil {
		t.Fatal(err)
	}
	if err := m.HandleMQTT("sms/v1/devices/deviceA/events", event); err == nil {
		t.Fatal("accepted replay")
	}
	if err := m.HandleMQTT("sms/v1/devices/deviceA/events", s.packet(t, "event", msg)); err != nil {
		t.Fatal(err)
	}
	var count int64
	m.DB.Model(&models.TextMessage{}).Count(&count)
	if count != 1 {
		t.Fatalf("saved duplicates: %d", count)
	}
	// Restart with the same DB and private key. Sequence numbers must not reset.
	restarted, err := New(m.DB, m.Config, zap.NewNop())
	if err != nil {
		t.Fatal(err)
	}
	restarted.mqtt = m.mqtt
	if err := restarted.HandleMQTT("sms/v1/devices/deviceA/events", event); err == nil {
		t.Fatal("accepted pre-restart replay")
	}
	if err := restarted.HandleMQTT("sms/v1/devices/deviceA/events", s.packet(t, "event", map[string]any{"message_id": "message2", "data": data})); err != nil {
		t.Fatal(err)
	}
	before := len(replies)
	s2 := newTestSession(t, m, "deviceA")
	request["registration_token"] = "wrong"
	if err := m.HandleMQTT("sms/v1/devices/deviceA/register", s2.packet(t, "register", request)); err == nil {
		t.Fatal("accepted invalid token")
	}
	if len(replies) != before {
		t.Fatal("published authentication oracle")
	}
	if err := restarted.Revoke(ctx, "deviceA"); err != nil {
		t.Fatal(err)
	}
	if err := restarted.HandleMQTT("sms/v1/devices/deviceA/events", s.packet(t, "event", msg)); err == nil {
		t.Fatal("accepted revoked session")
	}
}

func TestMQTTRejectsWrongTopicTamperingAndRetiredSession(t *testing.T) {
	m := testManager(t)
	token, _ := m.Provision(context.Background(), "deviceA")
	m.mqtt = &mqttTransport{publish: func(string, []byte) error { return nil }}
	register := func(s *testSession) []byte {
		return s.packet(t, "register", map[string]any{"registration_token": token, "key_id": "server-key-1", "features": map[string]any{"device_id": "deviceA"}})
	}
	s := newTestSession(t, m, "deviceA")
	packet := register(s)
	if m.HandleMQTT("sms/v1/devices/deviceB/register", packet) == nil {
		t.Fatal("accepted topic mismatch")
	}
	var p Envelope
	_ = json.Unmarshal(packet, &p)
	p.Tag = strings.Repeat("00", 16)
	if m.HandleMQTT("sms/v1/devices/deviceA/register", mustJSON(t, p)) == nil {
		t.Fatal("accepted corrupt tag")
	}
	if err := m.HandleMQTT("sms/v1/devices/deviceA/register", packet); err != nil {
		t.Fatal(err)
	}
	s2 := newTestSession(t, m, "deviceA")
	if err := m.HandleMQTT("sms/v1/devices/deviceA/register", register(s2)); err != nil {
		t.Fatal(err)
	}
	if m.HandleMQTT("sms/v1/devices/deviceA/register", packet) == nil {
		t.Fatal("reactivated retired session")
	}
}

func TestCommandRoutesAndResultCannotCrossDevices(t *testing.T) {
	m := testManager(t)
	a := addSerial(t, m, "A")
	b := addSerial(t, m, "B")
	ctx := context.Background()
	cmd, err := m.Command(ctx, "A", "send_sms", map[string]any{"to": "10086", "content": "hello"}, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(a.messages) != 1 || len(b.messages) != 0 {
		t.Fatal("command routed to wrong device")
	}
	result := mustJSON(t, map[string]any{"type": "sms_send_result", "device_id": "B", "request_id": cmd.ID, "success": true})
	if err := m.DB.Transaction(func(tx *gorm.DB) error { return m.eventTx(tx, "B", "", result) }); err != nil {
		t.Fatal(err)
	}
	var msg models.TextMessage
	_ = m.DB.First(&msg, "id = ?", cmd.ID).Error
	if msg.Status != models.MessageStatusSending {
		t.Fatal("B updated A SMS")
	}
	result = mustJSON(t, map[string]any{"type": "sms_send_result", "device_id": "A", "request_id": cmd.ID, "success": true})
	if err := m.DB.Transaction(func(tx *gorm.DB) error { return m.eventTx(tx, "A", "", result) }); err != nil {
		t.Fatal(err)
	}
	_ = m.DB.First(&msg, "id = ?", cmd.ID).Error
	if msg.Status != models.MessageStatusSent {
		t.Fatal(msg.Status)
	}
}

func TestDeviceHTTPIsolationAndAuthentication(t *testing.T) {
	m := testManager(t)
	addSerial(t, m, "A")
	addSerial(t, m, "B")
	for _, id := range []string{"A", "B"} {
		if err := m.DB.Create(&models.TextMessage{ID: "sms" + id, DeviceID: id, From: "10086", Content: id, Type: models.MessageTypeIncoming}).Error; err != nil {
			t.Fatal(err)
		}
	}
	e := echo.New()
	api := e.Group("/api")
	api.Use(middleware.JWTMiddleware("test-secret", zap.NewNop()))
	m.Register(api)
	// Use the same token utility as the existing middleware, not a bypass middleware.
	token, _, err := util.GenerateToken("operator", "test-secret", 1)
	if err != nil {
		t.Fatal(err)
	}
	request := func(method, path, body string, auth bool) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		if auth {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		rec := httptest.NewRecorder()
		e.ServeHTTP(rec, req)
		return rec
	}
	if rec := request("GET", "/api/devices", "", false); rec.Code != http.StatusUnauthorized {
		t.Fatal(rec.Code)
	}
	if rec := request("GET", "/api/devices/A/messages/conversations/10086/messages", "", true); rec.Code != 200 || strings.Contains(rec.Body.String(), `"content":"B"`) {
		t.Fatal(rec.Code, rec.Body.String())
	}
	if rec := request("DELETE", "/api/devices/A/messages/smsB", "", true); rec.Code != 404 {
		t.Fatal(rec.Code)
	}
	if rec := request("PUT", "/api/devices/A/properties/notification_channels", `{"value":[]}`, true); rec.Code != 204 {
		t.Fatal(rec.Code, rec.Body.String())
	}
	var settings []Setting
	m.DB.Where("device_id = ?", "B").Find(&settings)
	if len(settings) != 0 {
		t.Fatal("settings leaked to B")
	}
	if rec := request("GET", "/api/messages/conversations", "", true); rec.Code != 404 {
		t.Fatal("unscoped route exposed")
	}
}

func TestNotificationQueuesAreDeviceScopedAndDurable(t *testing.T) {
	m := testManager(t)
	addSerial(t, m, "A")
	addSerial(t, m, "B")
	channels := []models.NotificationChannelConfig{{Type: "webhook", Enabled: true, Config: map[string]any{"url": "https://example.invalid", "body": "{}", "contentType": "application/json"}}}
	if err := m.SetSetting(context.Background(), "A", service.PropertyIDNotificationChannels, "A", mustJSON(t, channels)); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"A", "B"} {
		err := m.DB.Transaction(func(tx *gorm.DB) error {
			return m.eventTx(tx, id, "same-message", mustJSON(t, map[string]any{"type": "incoming_sms", "device_id": id, "content": "x"}))
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	var jobs []Delivery
	m.DB.Find(&jobs)
	if len(jobs) != 1 || jobs[0].DeviceID != "A" {
		t.Fatalf("unexpected jobs %+v", jobs)
	}
	// Disabling A must cancel A's pending delivery instead of using stale queued credentials.
	if err := m.SetSetting(context.Background(), "A", service.PropertyIDNotificationChannels, "A", json.RawMessage(`[]`)); err != nil {
		t.Fatal(err)
	}
	var job notificationJob
	if err := json.Unmarshal([]byte(jobs[0].Payload), &job); err != nil {
		t.Fatal(err)
	}
	if err := m.deliverNotification(context.Background(), "A", job); err != nil {
		t.Fatal(err)
	}
}

type fakePort struct {
	mu     sync.Mutex
	chunks [][]byte
	writes [][]byte
	closed bool
}

func (p *fakePort) SetReadTimeout(time.Duration) error { return nil }
func (p *fakePort) Read(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, io.EOF
	}
	if len(p.chunks) == 0 {
		return 0, io.EOF
	}
	chunk := p.chunks[0]
	p.chunks = p.chunks[1:]
	return copy(b, chunk), nil
}
func (p *fakePort) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.writes = append(p.writes, append([]byte(nil), b...))
	return len(b), nil
}
func (p *fakePort) Close() error { p.mu.Lock(); defer p.mu.Unlock(); p.closed = true; return nil }

func TestSerialHandshakeAndFrameBoundaries(t *testing.T) {
	m := testManager(t)
	frame := `SMS_START:{"type":"status_response","device_id":"A","access_mode":"serial","features":{"imei":"123"},"mobile":{"number":"13800000000"}}:SMS_END` + "\r\n"
	port := &fakePort{chunks: [][]byte{[]byte(frame[:20]), []byte(frame[20:])}}
	err := m.serveSerial(context.Background(), "fake", port)
	if !errors.Is(err, io.EOF) {
		t.Fatal(err)
	}
	d, err := m.Device(context.Background(), "A")
	if err != nil {
		t.Fatal(err)
	}
	if d.Online || d.AccessMode != "serial" {
		t.Fatal(d)
	}
	if len(port.writes) != 1 || !bytes.Contains(port.writes[0], []byte("get_status")) {
		t.Fatal("probe had side effects")
	}
	bad := &fakePort{chunks: [][]byte{[]byte(`SMS_START:{"type":"status_response"}:SMS_END`)}}
	if m.serveSerial(context.Background(), "bad", bad) == nil {
		t.Fatal("accepted no identity")
	}
}

func TestLuaWireFixtureAndServerEncryption(t *testing.T) {
	data, err := os.ReadFile("testdata/lua_wire.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Root           string   `json:"root"`
		Up             Envelope `json:"up"`
		DownCiphertext string   `json:"down_ciphertext"`
		DownTag        string   `json:"down_tag"`
	}
	if err := json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	root, err := hex.DecodeString(f.Root)
	if err != nil {
		t.Fatal(err)
	}
	body, err := decryptPacket(root, f.Up)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(body, []byte("fixture-only-not-a-real-token")) {
		t.Fatal("Lua payload mismatch")
	}
	packet, err := encryptPacket(root, Session{ID: f.Up.SID, DeviceID: f.Up.DeviceID, DownSeq: 1}, "register_result", map[string]any{"accepted": true, "server_time": 1800000000})
	if err != nil {
		t.Fatal(err)
	}
	var p Envelope
	_ = json.Unmarshal(packet, &p)
	if p.Ciphertext != f.DownCiphertext || p.Tag != f.DownTag {
		t.Fatal("server encryption differs from independent Python AES-GCM implementation")
	}
}

func TestSerialDelimiterInSMSAndCoalescedProbeEvents(t *testing.T) {
	f := frameBuffer{}
	raw := `{"type":"incoming_sms","device_id":"A","content":"literal :SMS_END in body"}`
	frames, err := f.feed([]byte("SMS_START:" + raw + ":SMS_END\r\n"))
	if err != nil || len(frames) != 1 || string(frames[0]) != raw {
		t.Fatal(frames, err)
	}
	m := testManager(t)
	identity := `SMS_START:{"type":"status_response","device_id":"A","access_mode":"serial","features":{}}:SMS_END`
	port := &fakePort{chunks: [][]byte{[]byte(identity + "SMS_START:" + raw + ":SMS_END")}}
	_ = m.serveSerial(context.Background(), "fake", port)
	var count int64
	m.DB.Model(&models.TextMessage{}).Where("device_id = ?", "A").Count(&count)
	if count != 1 {
		t.Fatal("probe discarded SMS in same read")
	}
	p := &serialPeer{port: &fakePort{}}
	if err := p.Send(context.Background(), map[string]any{"content": "literal :CMD_END"}); err != nil {
		t.Fatal(err)
	}
	got := p.port.(*fakePort).writes[0]
	if bytes.Count(got, []byte(":CMD_END")) != 1 {
		t.Fatal("command body can break Lua delimiter framing")
	}
}

func TestDeviceTasksAndStatusResults(t *testing.T) {
	m := testManager(t)
	a := addSerial(t, m, "A")
	b := addSerial(t, m, "B")
	ctx := context.Background()
	task := models.ScheduledTask{ID: "taskA", DeviceID: "A", Name: "test", Enabled: true, IntervalDays: 90, PhoneNumber: "10086", Content: "hello"}
	if err := m.DB.Create(&task).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := m.TriggerTask(ctx, "B", task.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatal("B can trigger A task")
	}
	cmd, err := m.TriggerTask(ctx, "A", task.ID)
	if err != nil {
		t.Fatal(err)
	}
	if len(a.messages) != 1 || len(b.messages) != 0 {
		t.Fatal("task dispatched to wrong device")
	}
	if _, err := m.TriggerTask(ctx, "A", task.ID); err == nil {
		t.Fatal("duplicate pending task execution")
	}
	data := mustJSON(t, map[string]any{"type": "sms_send_result", "device_id": "A", "request_id": cmd.ID, "success": true})
	if err := m.DB.Transaction(func(tx *gorm.DB) error { return m.eventTx(tx, "A", "", data) }); err != nil {
		t.Fatal(err)
	}
	if err := m.DB.First(&task, "id = ?", task.ID).Error; err != nil {
		t.Fatal(err)
	}
	if task.LastRunStatus != models.LastRunStatusSuccess {
		t.Fatal(task.LastRunStatus)
	}
	status, err := m.Command(ctx, "A", "get_status", nil, "")
	if err != nil {
		t.Fatal(err)
	}
	data = mustJSON(t, map[string]any{"type": "status_response", "device_id": "A", "request_id": status.ID, "mobile": map[string]any{"number": "13800000000"}})
	if err := m.DB.Transaction(func(tx *gorm.DB) error { return m.eventTx(tx, "A", "", data) }); err != nil {
		t.Fatal(err)
	}
	if err := m.DB.First(&status, "id = ?", status.ID).Error; err != nil {
		t.Fatal(err)
	}
	if status.Status != "succeeded" {
		t.Fatal(status.Status)
	}
	d, _ := m.Device(ctx, "B")
	if strings.Contains(d.Status, "13800000000") {
		t.Fatal("status leaked to B")
	}
}

func TestDeploymentYAMLSyntax(t *testing.T) {
	for _, name := range []string{"config.example.yaml", "docker-compose.backend.yml", "docker-compose.tls.yml"} {
		data, err := os.ReadFile(filepath.Join("..", "..", name))
		if err != nil {
			t.Fatal(err)
		}
		var doc map[string]any
		if err := yaml.Unmarshal(data, &doc); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
	}
}
