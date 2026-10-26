package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/dushixiang/uart_sms_forwarder/internal/models"
	"github.com/dushixiang/uart_sms_forwarder/internal/service"
	"github.com/google/uuid"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func stringValue(v any) string { s, _ := v.(string); return s }

func mergeStatus(old string, event map[string]any) string {
	status := map[string]any{}
	_ = json.Unmarshal([]byte(old), &status)
	mobile, _ := status["mobile"].(map[string]any)
	if mobile == nil {
		mobile = map[string]any{}
	}
	if features, ok := event["features"].(map[string]any); ok {
		if info, ok := features["mobile"].(map[string]any); ok {
			for k, v := range info {
				mobile[k] = v
			}
		}
	}
	if info, ok := event["mobile"].(map[string]any); ok {
		for k, v := range info {
			mobile[k] = v
		}
	}
	for _, k := range []string{"rssi", "signal_level", "signal_desc", "sim_ready", "sms_ready", "flymode"} {
		if v, ok := event[k]; ok {
			mobile[k] = v
		}
	}
	if v, ok := event["net_reg"]; ok {
		mobile["is_registered"] = v
	}
	for _, k := range []string{"type", "version", "timestamp", "mem_kb", "memory_usage", "buffer_size", "locating"} {
		if v, ok := event[k]; ok {
			status[k] = v
		}
	}
	status["mobile"] = mobile
	if v, ok := mobile["flymode"]; ok {
		status["flymode"] = v
	}
	b, _ := json.Marshal(status)
	return string(b)
}

// eventTx commits event deduplication, SMS, command results, and notification jobs together.
func (m *Manager) eventTx(tx *gorm.DB, id, messageID string, data json.RawMessage) error {
	var e map[string]any
	if json.Unmarshal(data, &e) != nil || stringValue(e["type"]) == "" {
		return errors.New("invalid device event")
	}
	if eventID := stringValue(e["device_id"]); eventID != "" && eventID != id {
		return errors.New("event device mismatch")
	}
	var d Device
	if err := tx.First(&d, "id = ?", id).Error; err != nil {
		return err
	}
	if messageID != "" {
		if len(messageID) > 128 {
			return errors.New("message ID too long")
		}
		r := Receipt{DeviceID: id, MessageID: messageID}
		result := tx.Clauses(clause.OnConflict{DoNothing: true}).Create(&r)
		if result.Error != nil {
			return result.Error
		}
		if result.RowsAffected == 0 {
			return tx.Model(&Device{}).Where("id = ?", id).Updates(map[string]any{"online": true, "last_seen": m.now().UnixMilli()}).Error
		}
	}
	now := m.now().UnixMilli()
	updates := map[string]any{"online": true, "last_seen": now}
	typ := stringValue(e["type"])
	switch typ {
	case "heartbeat", "status_response", "system_ready":
		updates["status"] = mergeStatus(d.Status, e)
		if features, ok := e["features"].(map[string]any); ok {
			b, _ := json.Marshal(features)
			updates["features"] = string(b)
		}
		if typ == "status_response" && stringValue(e["request_id"]) != "" {
			if err := tx.Model(&Command{}).Where("id = ? AND device_id = ? AND action = ? AND status NOT IN ?", stringValue(e["request_id"]), id, "get_status", []string{"succeeded", "failed"}).Updates(map[string]any{"status": "succeeded", "result": string(data)}).Error; err != nil {
				return err
			}
		}
	case "incoming_sms":
		from, content := stringValue(e["from"]), stringValue(e["content"])
		if len(from) > 128 || len(content) > 16000 {
			return errors.New("SMS event too large")
		}
		msg := models.TextMessage{ID: uuid.NewString(), DeviceID: id, From: from, Content: content, Type: models.MessageTypeIncoming, Status: models.MessageStatusReceived}
		if err := tx.Create(&msg).Error; err != nil {
			return err
		}
		updates["last_sms_at"] = now
		if err := m.queueNotification(tx, id, service.NotificationMessage{Type: "sms", From: from, Content: content, Timestamp: now / 1000}); err != nil {
			return err
		}
	case "incoming_call":
		if err := m.queueNotification(tx, id, service.NotificationMessage{Type: "call", From: stringValue(e["from"]), Timestamp: now / 1000}); err != nil {
			return err
		}
	case "sms_send_result", "cmd_response", "error":
		request := stringValue(e["request_id"])
		if request != "" {
			var cmd Command
			if err := tx.First(&cmd, "id = ? AND device_id = ?", request, id).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					break
				}
				return err
			}
			state := stringValue(e["result"])
			if typ == "sms_send_result" {
				if ok, _ := e["success"].(bool); ok {
					state = "succeeded"
					updates["last_sms_at"] = now
				} else {
					state = "failed"
				}
			}
			if typ == "error" || state == "error" {
				state = "failed"
			}
			if state == "ok" || state == "reboot_requested" {
				state = "succeeded"
			}
			if state != "accepted" && state != "queued" && state != "succeeded" && state != "failed" {
				state = "unknown"
			}
			// Late accepted/queued/unknown replies cannot downgrade a final result.
			if cmd.Status == "succeeded" || cmd.Status == "failed" {
				break
			}
			if err := tx.Model(&Command{}).Where("id = ? AND device_id = ?", request, id).Updates(map[string]any{"status": state, "result": string(data)}).Error; err != nil {
				return err
			}
			if cmd.RestoreFlymode && (state == "succeeded" || state == "failed") {
				if err := tx.Model(&Command{}).Where("id = ?", cmd.ID).Update("restore_after", now+30000).Error; err != nil {
					return err
				}
			}
			if cmd.Action == "send_sms" && (state == "succeeded" || state == "failed" || state == "unknown") {
				status := "unknown"
				if state == "succeeded" {
					status = "sent"
				}
				if state == "failed" {
					status = "failed"
				}
				if err := tx.Model(&models.TextMessage{}).Where("id = ? AND device_id = ?", request, id).Update("status", status).Error; err != nil {
					return err
				}
				if cmd.TaskID != "" {
					taskStatus := models.LastRunStatusUnknown
					if state == "succeeded" {
						taskStatus = models.LastRunStatusSuccess
					}
					if state == "failed" {
						taskStatus = models.LastRunStatusFailed
					}
					if err := tx.Model(&models.ScheduledTask{}).Where("id = ? AND device_id = ? AND last_msg_id = ?", cmd.TaskID, id, request).Update("last_run_status", taskStatus).Error; err != nil {
						return err
					}
				}
				if state == "failed" {
					if err := m.queueNotification(tx, id, service.NotificationMessage{Type: "sms", From: "短信转发器", Content: "短信发送失败: " + stringValue(e["to"]), Timestamp: now / 1000}); err != nil {
						return err
					}
				}
			}
			if state == "succeeded" && cmd.Action == "set_locate" {
				if locating, ok := e["locating"].(bool); ok {
					updates["status"] = mergeStatus(d.Status, map[string]any{"locating": locating})
				}
			}
			if state == "succeeded" && cmd.Action == "set_flymode" {
				var args map[string]any
				_ = json.Unmarshal([]byte(cmd.Payload), &args)
				updates["status"] = mergeStatus(d.Status, map[string]any{"flymode": args["enabled"]})
			}
		}
	case "sim_event", "call_disconnected", "warning":
	default:
		return errors.New("unsupported event type")
	}
	return tx.Model(&Device{}).Where("id = ?", id).Updates(updates).Error
}

type notificationJob struct {
	Channel models.NotificationChannelConfig `json:"channel"`
	Message service.NotificationMessage      `json:"message"`
}

func validateChannel(c models.NotificationChannelConfig) error {
	if !c.Enabled {
		return nil
	}
	if c.Type == "bark" {
		_, err := service.BarkPushURL(c.Config)
		return err
	}
	required := map[string][]string{"dingtalk": {"secretKey"}, "wecom": {"secretKey"}, "feishu": {"secretKey"}, "webhook": {"url", "body", "contentType"}, "email": {"smtpHost", "username", "password", "from", "to"}, "telegram": {"apiToken", "userid"}}
	fields, ok := required[c.Type]
	if !ok {
		return errors.New("unsupported notification channel")
	}
	for _, k := range fields {
		if stringValue(c.Config[k]) == "" {
			return errors.New("missing notification field: " + k)
		}
	}
	return nil
}

func (m *Manager) queueNotification(tx *gorm.DB, id string, msg service.NotificationMessage) error {
	var setting Setting
	err := tx.First(&setting, "device_id = ? AND id = ?", id, service.PropertyIDNotificationChannels).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	var channels []models.NotificationChannelConfig
	if err := json.Unmarshal([]byte(setting.Value), &channels); err != nil {
		return err
	}
	for _, c := range channels {
		if !c.Enabled {
			continue
		}
		b, err := json.Marshal(notificationJob{c, msg})
		if err != nil {
			return err
		}
		if err := tx.Create(&Delivery{ID: uuid.NewString(), DeviceID: id, Payload: string(b)}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (m *Manager) notify(ctx context.Context, c models.NotificationChannelConfig, msg service.NotificationMessage) (err error) {
	defer func() {
		if recover() != nil {
			err = errors.New("invalid notification configuration")
		}
	}()
	if err := validateChannel(c); err != nil {
		return err
	}
	switch c.Type {
	case "bark":
		return m.notifier.SendBarkByConfig(ctx, c.Config, msg.String())
	case "dingtalk":
		return m.notifier.SendDingTalkByConfig(ctx, c.Config, msg.String())
	case "wecom":
		return m.notifier.SendWeComByConfig(ctx, c.Config, msg.String())
	case "feishu":
		return m.notifier.SendFeishuByConfig(ctx, c.Config, msg.String())
	case "webhook":
		return m.notifier.SendWebhookByConfig(ctx, c.Config, msg)
	case "email":
		return m.notifier.SendEmail(ctx, c.Config, msg)
	case "telegram":
		return m.notifier.SendTelegramByConfig(ctx, c.Config, msg.String())
	}
	return errors.New("unsupported notification channel")
}

func (m *Manager) TestNotification(ctx context.Context, id, kind string) error {
	s, err := m.GetSetting(ctx, id, service.PropertyIDNotificationChannels)
	if err != nil {
		return err
	}
	var channels []models.NotificationChannelConfig
	if err := json.Unmarshal([]byte(s.Value), &channels); err != nil {
		return err
	}
	for _, c := range channels {
		if c.Type == kind && c.Enabled {
			msg, err := m.deviceNotification(ctx, id, service.NotificationMessage{Type: "sms", From: id, Content: "这是一条测试通知消息", Timestamp: m.now().Unix()})
			if err != nil {
				return err
			}
			return m.notify(ctx, c, msg)
		}
	}
	return errors.New("enabled notification channel not found")
}

func (m *Manager) notifications(ctx context.Context) {
	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			var jobs []Delivery
			if err := m.DB.Where("done = ? AND attempts < ? AND next_attempt <= ?", false, 8, m.now().UnixMilli()).Order("created_at").Limit(10).Find(&jobs).Error; err != nil {
				m.log.Error("read notification queue", zap.Error(err))
				continue
			}
			for _, job := range jobs {
				var payload notificationJob
				err := json.Unmarshal([]byte(job.Payload), &payload)
				if err == nil {
					err = m.deliverNotification(ctx, job.DeviceID, payload)
				}
				update := map[string]any{"attempts": job.Attempts + 1, "done": err == nil, "next_attempt": m.now().Add(time.Duration(1<<uint(job.Attempts)) * 10 * time.Second).UnixMilli(), "last_error": ""}
				if err != nil {
					update["last_error"] = "notification delivery failed"
					m.log.Warn("notification delivery failed", zap.String("device_id", job.DeviceID), zap.String("delivery_id", job.ID))
				}
				if err := m.DB.Model(&Delivery{}).Where("id = ?", job.ID).Updates(update).Error; err != nil {
					m.log.Error("persist notification result", zap.Error(err))
				}
			}
		}
	}
}

// Resolve the current server-side alias, including for queued notifications.
func (m *Manager) deviceNotification(ctx context.Context, id string, msg service.NotificationMessage) (service.NotificationMessage, error) {
	device, err := m.Device(ctx, id)
	if err != nil {
		return msg, err
	}
	msg.DeviceName = strings.TrimSpace(device.Name)
	var features struct {
		IMEI string `json:"imei"`
	}
	_ = json.Unmarshal([]byte(device.Features), &features)
	if msg.DeviceName == "" || msg.DeviceName == device.ID || msg.DeviceName == features.IMEI {
		msg.DeviceName = "未命名设备"
	}
	return msg, nil
}

// Re-read only this device's current policy, honoring disabled channels and updated credentials.
func (m *Manager) deliverNotification(ctx context.Context, id string, job notificationJob) error {
	setting, err := m.GetSetting(ctx, id, service.PropertyIDNotificationChannels)
	if err != nil {
		return err
	}
	var channels []models.NotificationChannelConfig
	if err := json.Unmarshal([]byte(setting.Value), &channels); err != nil {
		return err
	}
	for _, channel := range channels {
		if channel.Type == job.Channel.Type && channel.Enabled {
			msg, err := m.deviceNotification(ctx, id, job.Message)
			if err != nil {
				return err
			}
			return m.notify(ctx, channel, msg)
		}
	}
	return nil // Explicitly disabled/deleted channel: cancel this pending delivery.
}
