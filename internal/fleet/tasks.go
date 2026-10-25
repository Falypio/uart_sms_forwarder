package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/dushixiang/uart_sms_forwarder/internal/models"
	"github.com/dushixiang/uart_sms_forwarder/internal/service"
	"go.uber.org/zap"
)

var errTaskUncertain = errors.New("previous task command pending or uncertain; review its result first")

func (m *Manager) TriggerTask(ctx context.Context, id, taskID string) (Command, error) {
	m.scheduleMu.Lock()
	defer m.scheduleMu.Unlock()
	var task models.ScheduledTask
	if err := m.DB.WithContext(ctx).First(&task, "id = ? AND device_id = ?", taskID, id).Error; err != nil {
		return Command{}, err
	}
	if task.LastMsgId != "" {
		var pending int64
		if err := m.DB.Model(&Command{}).Where("id = ? AND status IN ?", task.LastMsgId, []string{"pending", "queued", "accepted", "unknown"}).Count(&pending).Error; err != nil {
			return Command{}, err
		}
		if pending > 0 {
			return Command{}, errTaskUncertain
		}
	}
	return m.Command(ctx, id, "send_sms", map[string]any{"to": task.PhoneNumber, "content": task.Content}, task.ID)
}

func (m *Manager) runScheduled(ctx context.Context) {
	now := m.now()
	if now.Hour() < 8 {
		return
	}
	var tasks []models.ScheduledTask
	if err := m.DB.Where("enabled = ?", true).Find(&tasks).Error; err != nil {
		m.log.Error("read scheduled tasks", zap.Error(err))
		return
	}
	for _, t := range tasks {
		days := t.IntervalDays
		if t.LastRunStatus == models.LastRunStatusFailed {
			days = 1
		}
		if t.LastRunAt > 0 && now.Sub(time.UnixMilli(t.LastRunAt)) < time.Duration(days)*24*time.Hour {
			continue
		}
		if _, err := m.TriggerTask(ctx, t.DeviceID, t.ID); err != nil {
			if errors.Is(err, errTaskUncertain) {
				continue
			}
			m.log.Debug("scheduled SMS not dispatched", zap.String("device_id", t.DeviceID), zap.String("task_id", t.ID))
			_ = m.DB.Model(&models.ScheduledTask{}).Where("id = ? AND last_run_at = ?", t.ID, t.LastRunAt).Updates(map[string]any{"last_run_at": now.UnixMilli(), "last_run_status": models.LastRunStatusFailed}).Error
		}
	}
}

func (m *Manager) autoFlymode(ctx context.Context) {
	var devices []Device
	if m.DB.Where("access_mode = ? AND online = ?", "serial", true).Find(&devices).Error != nil {
		return
	}
	for _, d := range devices {
		setting, err := m.GetSetting(ctx, d.ID, service.PropertyIDAutoFlymodeConfig)
		if err != nil {
			continue
		}
		var cfg models.AutoFlymodeConfig
		if json.Unmarshal([]byte(setting.Value), &cfg) != nil {
			continue
		}
		if !cfg.Enabled {
			if d.AutoFlymode {
				if _, err := m.Command(ctx, d.ID, "set_flymode", map[string]any{"enabled": false}, ""); err == nil {
				}
			}
			m.idleEnabled[d.ID] = false
			continue
		}
		if !m.idleEnabled[d.ID] {
			m.idleEnabled[d.ID] = true
			_ = m.DB.Model(&Device{}).Where("id = ?", d.ID).Update("last_sms_at", m.now().UnixMilli()).Error
			continue
		}
		var pending int64
		_ = m.DB.Model(&Command{}).Where("device_id = ? AND status IN ?", d.ID, []string{"pending", "queued", "accepted"}).Count(&pending).Error
		var status map[string]any
		_ = json.Unmarshal([]byte(d.Status), &status)
		if status["flymode"] == true || pending > 0 {
			continue
		}
		if m.now().Sub(time.UnixMilli(d.LastSMSAt)) >= time.Duration(cfg.IdleTimeoutHours)*time.Hour {
			if _, err := m.Command(ctx, d.ID, "set_flymode", map[string]any{"enabled": true}, ""); err == nil {
				_ = m.DB.Model(&Device{}).Where("id = ?", d.ID).Update("auto_flymode", true).Error
			}
		}
	}
}

func (m *Manager) restoreFlymodes(ctx context.Context) {
	var commands []Command
	if m.DB.Where("restore_flymode = ? AND restore_after <= ?", true, m.now().UnixMilli()).Find(&commands).Error != nil {
		return
	}
	for _, c := range commands {
		var pending int64
		_ = m.DB.Model(&Command{}).Where("device_id = ? AND action = ? AND status IN ?", c.DeviceID, "send_sms", []string{"pending", "accepted", "queued"}).Count(&pending).Error
		if pending > 0 {
			continue
		}
		if _, err := m.Command(ctx, c.DeviceID, "set_flymode", map[string]any{"enabled": true}, ""); err == nil {
			_ = m.DB.Model(&Command{}).Where("device_id = ?", c.DeviceID).Update("restore_flymode", false).Error
		}
	}
}
