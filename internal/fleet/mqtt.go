package fleet

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"gorm.io/gorm"
)

type mqttInput struct {
	topic   string
	payload []byte
}
type mqttTransport struct {
	m      *Manager
	client paho.Client
	inputs chan mqttInput
	// Overridable publisher for offline protocol tests. Never use a test Broker.
	publish func(string, []byte) error
}
type mqttPeer struct {
	m  *Manager
	id string
}

func (p *mqttPeer) Send(ctx context.Context, body map[string]any) error {
	return p.m.sendMQTT(ctx, p.id, "command", body)
}
func (p *mqttPeer) Close() {}

func newMQTT(m *Manager) (*mqttTransport, error) {
	cfg := m.Config.MQTT
	u, err := url.Parse(cfg.Broker)
	if err != nil || u.Host == "" || u.User != nil || u.RawQuery != "" || u.Path != "" {
		return nil, errors.New("MQTT Broker must be tcp://host:port or ssl://host:port")
	}
	if cfg.TLS && u.Scheme != "ssl" || !cfg.TLS && u.Scheme != "tcp" {
		return nil, errors.New("MQTT TLS flag must match Broker scheme")
	}
	if cfg.Username == "" || cfg.Password == "" {
		return nil, errors.New("MQTT username/password required")
	}
	if cfg.TopicPrefix == "" || strings.ContainsAny(cfg.TopicPrefix, "+#") || strings.HasSuffix(cfg.TopicPrefix, "/") {
		return nil, errors.New("invalid MQTT topic prefix")
	}
	t := &mqttTransport{m: m, inputs: make(chan mqttInput, 256)}
	opts := paho.NewClientOptions().AddBroker(cfg.Broker).SetClientID(cfg.ClientID).SetUsername(cfg.Username).SetPassword(cfg.Password).
		SetAutoReconnect(true).SetConnectTimeout(10 * time.Second).SetWriteTimeout(5 * time.Second).SetOrderMatters(false).SetCleanSession(true)
	if cfg.ClientID == "" {
		opts.SetClientID("sms_backend")
	}
	if cfg.TLS {
		pool, err := x509.SystemCertPool()
		if err != nil {
			pool = x509.NewCertPool()
		}
		if cfg.CAFile != "" {
			data, err := os.ReadFile(cfg.CAFile)
			if err != nil {
				return nil, err
			}
			if !pool.AppendCertsFromPEM(data) {
				return nil, errors.New("invalid MQTT CA")
			}
		}
		opts.SetTLSConfig(&tls.Config{MinVersion: tls.VersionTLS12, RootCAs: pool, ServerName: u.Hostname()})
	}
	opts.SetOnConnectHandler(func(c paho.Client) {
		topics := map[string]byte{}
		for _, suffix := range []string{"register", "events", "results", "status"} {
			topics[cfg.TopicPrefix+"/+/"+suffix] = 1
		}
		token := c.SubscribeMultiple(topics, func(_ paho.Client, msg paho.Message) {
			if msg.Retained() || len(msg.Payload()) > 32768 {
				return
			}
			input := mqttInput{msg.Topic(), append([]byte(nil), msg.Payload()...)}
			select {
			case t.inputs <- input:
			default:
				m.log.Warn("MQTT receive queue full")
			}
		})
		go func() {
			if !token.WaitTimeout(10*time.Second) || token.Error() != nil {
				m.log.Error("MQTT subscriptions failed")
				c.Disconnect(0)
			}
		}()
	})
	opts.SetConnectionLostHandler(func(_ paho.Client, _ error) {
		m.log.Warn("MQTT broker disconnected")
		_ = m.DB.Model(&Device{}).Where("access_mode = ?", "mqtt").Update("online", false).Error
	})
	t.client = paho.NewClient(opts)
	t.publish = func(topic string, data []byte) error {
		if !t.client.IsConnectionOpen() {
			return errors.New("MQTT broker offline")
		}
		token := t.client.Publish(topic, 1, false, data)
		if !token.WaitTimeout(5 * time.Second) {
			return errors.New("MQTT publish timeout")
		}
		return token.Error()
	}
	return t, nil
}

func (t *mqttTransport) run(ctx context.Context) {
	go func() {
		for ctx.Err() == nil {
			token := t.client.Connect()
			if token.WaitTimeout(15*time.Second) && token.Error() == nil {
				return
			}
			t.m.log.Warn("MQTT initial connection failed; retrying")
			select {
			case <-ctx.Done():
				return
			case <-time.After(5 * time.Second):
			}
		}
	}()
	defer t.client.Disconnect(200)
	for {
		select {
		case <-ctx.Done():
			return
		case input := <-t.inputs:
			if err := t.m.HandleMQTT(input.topic, input.payload); err != nil {
				t.m.log.Warn("rejected MQTT packet")
			}
		}
	}
}

// HandleMQTT accepts only the documented Lua wire protocol; failures never publish decryption details.
func (m *Manager) HandleMQTT(topic string, payload []byte) error {
	m.mqttMu.Lock()
	defer m.mqttMu.Unlock()
	if len(payload) > 32768 {
		return errPacket
	}
	rest, ok := strings.CutPrefix(topic, m.Config.MQTT.TopicPrefix+"/")
	if !ok {
		return errPacket
	}
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || !validID.MatchString(parts[0]) {
		return errPacket
	}
	var p Envelope
	if json.Unmarshal(payload, &p) != nil || p.DeviceID != parts[0] {
		return errPacket
	}
	if parts[1] == "register" && p.Kind == "register" {
		return m.registerMQTT(p)
	}
	if p.Kind != "event" || (parts[1] != "events" && parts[1] != "results" && parts[1] != "status") {
		return errPacket
	}
	var session Session
	if err := m.DB.First(&session, "id = ? AND device_id = ? AND retired = ?", p.SID, p.DeviceID, false).Error; err != nil {
		return errPacket
	}
	var d Device
	if err := m.DB.First(&d, "id = ? AND active_sid = ? AND access_mode = ?", p.DeviceID, p.SID, "mqtt").Error; err != nil {
		return errPacket
	}
	if session.KeyID != m.Config.MQTT.KeyID || p.Seq <= session.UpSeq {
		return errPacket
	}
	root, err := unwrap(m.private, session.WrappedKey)
	if err != nil {
		return errPacket
	}
	body, err := decryptPacketWithAAD(root, p, session.AADMode)
	if err != nil {
		return errPacket
	}
	var event struct {
		MessageID string          `json:"message_id"`
		Data      json.RawMessage `json:"data"`
	}
	if json.Unmarshal(body, &event) != nil || event.MessageID == "" {
		return errPacket
	}
	err = m.DB.Transaction(func(tx *gorm.DB) error {
		if err := m.eventTx(tx, p.DeviceID, event.MessageID, event.Data); err != nil {
			return err
		}
		return tx.Model(&Session{}).Where("id = ?", p.SID).Update("up_seq", p.Seq).Error
	})
	if err != nil {
		return err
	}
	if err := m.attach(p.DeviceID, "mqtt", &mqttPeer{m, p.DeviceID}); err != nil {
		return err
	}
	return m.sendDownLocked(p.DeviceID, "ack", map[string]any{"message_id": event.MessageID})
}

func (m *Manager) registerMQTT(p Envelope) error {
	if p.KeyID != m.Config.MQTT.KeyID {
		return errPacket
	}
	root, err := unwrap(m.private, p.WrappedKey)
	if err != nil {
		return errPacket
	}
	aadMode := ""
	body, err := decryptPacket(root, p)
	if err != nil {
		aadMode = luatFixedAADMode
		body, err = decryptPacketWithAAD(root, p, aadMode)
	}
	if err != nil {
		return errPacket
	}
	var request struct {
		Token    string         `json:"registration_token"`
		KeyID    string         `json:"key_id"`
		Features map[string]any `json:"features"`
	}
	if json.Unmarshal(body, &request) != nil || request.KeyID != p.KeyID || stringValue(request.Features["device_id"]) != p.DeviceID {
		return errPacket
	}
	var credential Credential
	if err := m.DB.First(&credential, "device_id = ? AND revoked = ?", p.DeviceID, false).Error; err != nil {
		return errPacket
	}
	hash := sha256.Sum256([]byte(request.Token))
	if subtle.ConstantTimeCompare([]byte(credential.TokenHash), []byte(hex.EncodeToString(hash[:]))) != 1 {
		return errPacket
	}
	m.mu.RLock()
	live, exists := m.connections[p.DeviceID]
	m.mu.RUnlock()
	if exists && live.mode == "serial" {
		return errors.New("device has active serial connection")
	}
	features, _ := json.Marshal(request.Features)
	err = m.DB.Transaction(func(tx *gorm.DB) error {
		var existing Session
		err := tx.First(&existing, "id = ?", p.SID).Error
		if err == nil {
			if existing.DeviceID != p.DeviceID || existing.Retired || existing.UpSeq != p.Seq || existing.WrappedKey != p.WrappedKey || existing.AADMode != aadMode {
				return errPacket
			}
			var d Device
			if tx.First(&d, "id = ? AND active_sid = ?", p.DeviceID, p.SID).Error != nil {
				return errPacket
			}
			return nil // retransmitted initial registration; never reset sequence counters
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if err := tx.Model(&Session{}).Where("device_id = ?", p.DeviceID).Update("retired", true).Error; err != nil {
			return err
		}
		s := Session{ID: p.SID, DeviceID: p.DeviceID, WrappedKey: p.WrappedKey, KeyID: p.KeyID, UpSeq: p.Seq, AADMode: aadMode}
		if err := tx.Create(&s).Error; err != nil {
			return err
		}
		var d Device
		err = tx.First(&d, "id = ?", p.DeviceID).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			d = Device{ID: p.DeviceID, Name: stringValue(request.Features["name"]), LastSMSAt: m.now().UnixMilli()}
		} else if err != nil {
			return err
		}
		d.AccessMode = "mqtt"
		d.Port = ""
		d.ActiveSID = p.SID
		d.Online = true
		d.LastSeen = m.now().UnixMilli()
		d.Features = string(features)
		d.Status = mergeStatus(d.Status, map[string]any{"features": request.Features})
		return tx.Save(&d).Error
	})
	if err != nil {
		return err
	}
	if err := m.attach(p.DeviceID, "mqtt", &mqttPeer{m, p.DeviceID}); err != nil {
		return err
	}
	return m.sendDownLocked(p.DeviceID, "register_result", map[string]any{"accepted": true, "server_time": m.now().Unix()})
}

func (m *Manager) sendMQTT(ctx context.Context, id, kind string, body any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	m.mqttMu.Lock()
	defer m.mqttMu.Unlock()
	return m.sendDownLocked(id, kind, body)
}

func (m *Manager) sendDownLocked(id, kind string, body any) error {
	if m.mqtt == nil || m.mqtt.publish == nil {
		return errors.New("MQTT service disabled")
	}
	var session Session
	err := m.DB.Transaction(func(tx *gorm.DB) error {
		var d Device
		if err := tx.First(&d, "id = ? AND access_mode = ?", id, "mqtt").Error; err != nil {
			return err
		}
		if err := tx.First(&session, "id = ? AND retired = ?", d.ActiveSID, false).Error; err != nil {
			return err
		}
		if session.DownSeq >= 2147483647 {
			return errors.New("session exhausted; reconnect device")
		}
		session.DownSeq++ // persist before publish; a failed send still consumes the nonce
		return tx.Model(&Session{}).Where("id = ?", session.ID).Update("down_seq", session.DownSeq).Error
	})
	if err != nil {
		return err
	}
	root, err := unwrap(m.private, session.WrappedKey)
	if err != nil {
		return errPacket
	}
	packet, err := encryptPacket(root, session, kind, body)
	if err != nil {
		return err
	}
	if len(packet) > 32768 {
		return fmt.Errorf("encrypted command too large")
	}
	return m.mqtt.publish(m.Config.MQTT.TopicPrefix+"/"+id+"/down", packet)
}
