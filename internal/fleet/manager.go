package fleet

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/dushixiang/uart_sms_forwarder/config"
	"github.com/dushixiang/uart_sms_forwarder/internal/models"
	"github.com/dushixiang/uart_sms_forwarder/internal/service"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type transport interface {
	Send(context.Context, map[string]any) error
	Close()
}
type connection struct {
	transport transport
	mode      string
}

type Manager struct {
	DB          *gorm.DB
	Config      config.AppConfig
	log         *zap.Logger
	private     *rsa.PrivateKey
	mu          sync.RWMutex
	connections map[string]connection
	ports       map[string]bool
	mqttMu      sync.Mutex // serialize session sequence allocation and all MQTT dispatch
	mqtt        *mqttTransport
	notifier    *service.Notifier
	scheduleMu  sync.Mutex
	controlMu   sync.Mutex
	now         func() time.Time
	idleEnabled map[string]bool
}

func New(db *gorm.DB, cfg config.AppConfig, logger *zap.Logger) (*Manager, error) {
	if cfg.MQTT.PrivateKeyFile == "" {
		cfg.MQTT.PrivateKeyFile = "./data/keys/server_private.pem"
	}
	if cfg.MQTT.KeyID == "" {
		cfg.MQTT.KeyID = "server-key-1"
	}
	if cfg.MQTT.TopicPrefix == "" {
		cfg.MQTT.TopicPrefix = "sms/v1/devices"
	}
	if cfg.MQTT.OfflineSeconds <= 0 {
		cfg.MQTT.OfflineSeconds = 180
	}
	if cfg.Serial.ScanIntervalSeconds <= 0 {
		cfg.Serial.ScanIntervalSeconds = 10
	}
	if err := db.AutoMigrate(&Device{}, &Credential{}, &Session{}, &Command{}, &Setting{}, &Receipt{}, &Delivery{}, &models.TextMessage{}, &models.ScheduledTask{}); err != nil {
		return nil, err
	}
	key, err := loadPrivateKey(cfg.MQTT.PrivateKeyFile)
	if err != nil {
		return nil, err
	}
	if err := db.Model(&Device{}).Where("online = ?", true).Update("online", false).Error; err != nil {
		return nil, err
	}
	return &Manager{DB: db, Config: cfg, private: key, log: logger, connections: map[string]connection{}, ports: map[string]bool{}, notifier: service.NewNotifier(logger), now: time.Now, idleEnabled: map[string]bool{}}, nil
}

func (m *Manager) Start(ctx context.Context) error {
	if m.Config.MQTT.Enabled {
		t, err := newMQTT(m)
		if err != nil {
			return err
		}
		m.mqtt = t
		go t.run(ctx)
	}
	if !m.Config.Serial.Disabled {
		go m.scanSerial(ctx)
	}
	go m.maintenance(ctx)
	go m.notifications(ctx)
	return nil
}

func (m *Manager) PublicKey() ([]byte, error) { return publicPEM(m.private) }

func (m *Manager) Provision(ctx context.Context, id string) (string, error) {
	m.mqttMu.Lock()
	defer m.mqttMu.Unlock()
	if !validID.MatchString(id) {
		return "", errors.New("invalid device ID")
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	hash := sha256.Sum256([]byte(token))
	err := m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		c := Credential{DeviceID: id, TokenHash: hex.EncodeToString(hash[:])}
		if err := tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "device_id"}}, DoUpdates: clause.AssignmentColumns([]string{"token_hash", "revoked"})}).Create(&c).Error; err != nil {
			return err
		}
		if err := tx.Model(&Session{}).Where("device_id = ?", id).Update("retired", true).Error; err != nil {
			return err
		}
		return tx.Model(&Device{}).Where("id = ? AND access_mode = ?", id, "mqtt").Updates(map[string]any{"online": false, "active_sid": ""}).Error
	})
	if err == nil {
		m.detachMode(id, "mqtt")
	}
	return token, err
}

func (m *Manager) Revoke(ctx context.Context, id string) error {
	m.mqttMu.Lock()
	defer m.mqttMu.Unlock()
	err := m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Model(&Credential{}).Where("device_id = ?", id).Update("revoked", true).Error; err != nil {
			return err
		}
		if err := tx.Model(&Session{}).Where("device_id = ?", id).Update("retired", true).Error; err != nil {
			return err
		}
		return tx.Model(&Device{}).Where("id = ? AND access_mode = ?", id, "mqtt").Updates(map[string]any{"online": false, "active_sid": ""}).Error
	})
	if err == nil {
		m.detachMode(id, "mqtt")
	}
	return err
}

func (m *Manager) attach(id, mode string, t transport) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.connections[id]; ok && old.transport != t {
		// A second live serial port must not steal another device connection.
		if old.mode == "serial" {
			return errors.New("device already has a live serial connection")
		}
		old.transport.Close()
	}
	m.connections[id] = connection{t, mode}
	return nil
}

func (m *Manager) detachMode(id, mode string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if old, ok := m.connections[id]; ok && old.mode == mode {
		old.transport.Close()
		delete(m.connections, id)
	}
}

func (m *Manager) transport(id string) (transport, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	c, ok := m.connections[id]
	if !ok {
		return nil, errors.New("device offline")
	}
	return c.transport, nil
}

func (m *Manager) Device(ctx context.Context, id string) (Device, error) {
	var d Device
	err := m.DB.WithContext(ctx).First(&d, "id = ?", id).Error
	return d, err
}

func (m *Manager) SetSetting(ctx context.Context, id, key, name string, value json.RawMessage) error {
	if key != service.PropertyIDNotificationChannels && key != service.PropertyIDAutoFlymodeConfig {
		return errors.New("unsupported property")
	}
	if key == service.PropertyIDAutoFlymodeConfig {
		var cfg models.AutoFlymodeConfig
		if json.Unmarshal(value, &cfg) != nil {
			return errors.New("invalid auto flymode config")
		}
		if err := service.ValidateAutoFlymodeConfig(cfg); err != nil {
			return err
		}
		// A timed restore is mandatory over MQTT; indefinite idle flight mode is serial-only.
		d, err := m.Device(ctx, id)
		if err != nil {
			return err
		}
		if cfg.Enabled && d.AccessMode == "mqtt" {
			return errors.New("automatic indefinite flymode is only supported for serial devices")
		}
	}
	if key == service.PropertyIDNotificationChannels {
		var channels []models.NotificationChannelConfig
		if json.Unmarshal(value, &channels) != nil || len(channels) > 20 {
			return errors.New("invalid notification channels")
		}
		seen := map[string]bool{}
		for _, c := range channels {
			if seen[c.Type] {
				return errors.New("each notification channel type may be configured only once per device")
			}
			seen[c.Type] = true
			if err := validateChannel(c); err != nil {
				return err
			}
		}
	}
	setting := Setting{DeviceID: id, ID: key, Name: name, Value: string(value)}
	return m.DB.WithContext(ctx).Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "device_id"}, {Name: "id"}}, DoUpdates: clause.AssignmentColumns([]string{"name", "value"})}).Create(&setting).Error
}

func (m *Manager) GetSetting(ctx context.Context, id, key string) (Setting, error) {
	var s Setting
	err := m.DB.WithContext(ctx).First(&s, "device_id = ? AND id = ?", id, key).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		s = Setting{DeviceID: id, ID: key, Name: key}
		switch key {
		case service.PropertyIDNotificationChannels:
			s.Value = "[]"
		case service.PropertyIDAutoFlymodeConfig:
			b, _ := json.Marshal(service.DefaultAutoFlymodeConfig())
			s.Value = string(b)
		default:
			return s, err
		}
		return s, nil
	}
	return s, err
}

func (m *Manager) maintenance(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			m.mu.Lock()
			for _, c := range m.connections {
				c.transport.Close()
			}
			m.connections = map[string]connection{}
			m.mu.Unlock()
			return
		case <-ticker.C:
			now := m.now()
			// Timed-out commands remain unknown, never automatically resend a paid SMS.
			if err := m.DB.Transaction(func(tx *gorm.DB) error {
				var commands []Command
				if err := tx.Where("status IN ? AND expires_at < ?", []string{"pending", "accepted", "queued"}, now.Add(-30*time.Second).Unix()).Find(&commands).Error; err != nil {
					return err
				}
				for _, c := range commands {
					if err := tx.Model(&Command{}).Where("id = ?", c.ID).Update("status", "unknown").Error; err != nil {
						return err
					}
					if c.Action == "send_sms" {
						if err := tx.Model(&models.TextMessage{}).Where("id = ? AND device_id = ? AND status = ?", c.ID, c.DeviceID, "sending").Update("status", "unknown").Error; err != nil {
							return err
						}
					}
					if c.TaskID != "" {
						if err := tx.Model(&models.ScheduledTask{}).Where("id = ? AND device_id = ? AND last_msg_id = ?", c.TaskID, c.DeviceID, c.ID).Update("last_run_status", models.LastRunStatusUnknown).Error; err != nil {
							return err
						}
					}
				}
				return nil
			}); err != nil {
				m.log.Error("command expiry failed", zap.Error(err))
			}
			var offline []Device
			if err := m.DB.Where("access_mode = ? AND online = ? AND last_seen < ?", "mqtt", true, now.Add(-time.Duration(m.Config.MQTT.OfflineSeconds)*time.Second).UnixMilli()).Find(&offline).Error; err == nil {
				for _, d := range offline {
					m.mqttMu.Lock()
					_ = m.DB.Model(&Device{}).Where("id = ? AND last_seen < ?", d.ID, now.Add(-time.Duration(m.Config.MQTT.OfflineSeconds)*time.Second).UnixMilli()).Update("online", false).Error
					m.mqttMu.Unlock()
				}
			}
			m.runScheduled(ctx)
			m.autoFlymode(ctx)
			m.restoreFlymodes(ctx)
		}
	}
}

func smsValid(to, content string) bool {
	if len(to) == 0 || len(to) > 24 || len(content) == 0 || len(content) > 4096 {
		return false
	}
	for i, r := range to {
		if r == '+' && i == 0 {
			continue
		}
		if r < '0' || r > '9' {
			return false
		}
	}
	return to != "+"
}

func (m *Manager) Command(ctx context.Context, id, action string, args map[string]any, taskID string) (Command, error) {
	m.controlMu.Lock()
	defer m.controlMu.Unlock()
	d, err := m.Device(ctx, id)
	if err != nil {
		return Command{}, err
	}
	if !d.Online {
		return Command{}, errors.New("device offline")
	}
	t, err := m.transport(id)
	if err != nil {
		return Command{}, err
	}
	if args == nil {
		args = map[string]any{}
	}
	switch action {
	case "send_sms":
		to, _ := args["to"].(string)
		body, _ := args["content"].(string)
		if !smsValid(to, body) {
			return Command{}, errors.New("invalid phone/content")
		}
	case "set_locate":
		if _, ok := args["enabled"].(bool); !ok {
			return Command{}, errors.New("enabled must be boolean")
		}
	case "set_flymode":
		enabled, ok := args["enabled"].(bool)
		if !ok {
			return Command{}, errors.New("enabled must be boolean")
		}
		if enabled && d.AccessMode == "mqtt" {
			duration, ok := number(args["duration_seconds"])
			if !ok || duration < 1 || duration > 86400 || duration != float64(int64(duration)) {
				return Command{}, errors.New("MQTT flymode requires duration_seconds 1..86400")
			}
		}
	case "get_status", "reset_stack", "reboot_mcu":
	default:
		return Command{}, errors.New("unsupported action")
	}
	// Serial can be awakened locally; MQTT cannot be reached while already offline.
	restoreFlight := false
	if action == "send_sms" && d.AccessMode == "serial" {
		var status map[string]any
		_ = json.Unmarshal([]byte(d.Status), &status)
		mobile, _ := status["mobile"].(map[string]any)
		flight, _ := mobile["flymode"].(bool)
		if flight {
			restoreFlight = !d.AutoFlymode
			wake := map[string]any{"device_id": id, "request_id": uuid.NewString(), "action": "set_flymode", "enabled": false}
			if err := t.Send(ctx, wake); err != nil {
				return Command{}, err
			}
		}
	}
	now := m.now()
	cmd := Command{ID: uuid.NewString(), DeviceID: id, Action: action, Status: "pending", ExpiresAt: now.Add(120 * time.Second).Unix(), TaskID: taskID}
	cmd.RestoreFlymode = restoreFlight
	cmd.RestoreAfter = now.Add(180 * time.Second).UnixMilli()
	body := map[string]any{}
	for k, v := range args {
		body[k] = v
	}
	body["device_id"] = id
	body["request_id"] = cmd.ID
	body["action"] = action
	body["expires_at"] = cmd.ExpiresAt
	b, err := json.Marshal(body)
	if err != nil {
		return Command{}, err
	}
	cmd.Payload = string(b)
	err = m.DB.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		if err := tx.Create(&cmd).Error; err != nil {
			return err
		}
		if action == "send_sms" {
			to := args["to"].(string)
			content := args["content"].(string)
			if err := tx.Create(&models.TextMessage{ID: cmd.ID, DeviceID: id, To: to, Content: content, Type: models.MessageTypeOutgoing, Status: models.MessageStatusSending}).Error; err != nil {
				return err
			}
		}
		if action == "send_sms" {
			if err := tx.Model(&Device{}).Where("id = ?", id).Updates(map[string]any{"last_sms_at": now.UnixMilli(), "auto_flymode": false}).Error; err != nil {
				return err
			}
		}
		if action == "set_flymode" {
			if err := tx.Model(&Device{}).Where("id = ?", id).Updates(map[string]any{"auto_flymode": false, "last_sms_at": now.UnixMilli()}).Error; err != nil {
				return err
			}
			if err := tx.Model(&Command{}).Where("device_id = ? AND restore_flymode = ?", id, true).Update("restore_flymode", false).Error; err != nil {
				return err
			}
		}
		if taskID != "" {
			return tx.Model(&models.ScheduledTask{}).Where("id = ? AND device_id = ?", taskID, id).Updates(map[string]any{"last_msg_id": cmd.ID, "last_run_at": now.UnixMilli(), "last_run_status": models.LastRunStatusUnknown}).Error
		}
		return nil
	})
	if err != nil {
		return Command{}, err
	}
	if err = t.Send(ctx, body); err != nil {
		// A transport timeout may happen after the device received the command.
		_ = m.DB.Model(&Command{}).Where("id = ? AND status = ?", cmd.ID, "pending").Update("status", "unknown").Error
		if action == "send_sms" {
			_ = m.DB.Model(&models.TextMessage{}).Where("id = ? AND status = ?", cmd.ID, "sending").Update("status", "unknown").Error
		}
		return cmd, fmt.Errorf("delivery uncertain; inspect command %s: %w", cmd.ID, err)
	}
	return cmd, nil
}

func number(v any) (float64, bool) {
	switch n := v.(type) {
	case float64:
		return n, true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	}
	return 0, false
}
