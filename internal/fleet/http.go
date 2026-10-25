package fleet

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"sort"
	"time"

	"github.com/dushixiang/uart_sms_forwarder/internal/models"
	"github.com/dushixiang/uart_sms_forwarder/internal/service"
	"github.com/google/uuid"
	"github.com/labstack/echo/v5"
	"gorm.io/gorm"
)

func apiError(c *echo.Context, err error) error {
	code := http.StatusBadRequest
	if errors.Is(err, gorm.ErrRecordNotFound) {
		code = http.StatusNotFound
	}
	return c.JSON(code, map[string]string{"error": err.Error()})
}

func deviceView(d Device) map[string]any {
	var features, status any
	_ = json.Unmarshal([]byte(d.Features), &features)
	_ = json.Unmarshal([]byte(d.Status), &status)
	return map[string]any{"id": d.ID, "name": d.Name, "accessMode": d.AccessMode, "online": d.Online, "port": d.Port, "lastSeen": d.LastSeen, "createdAt": d.CreatedAt, "features": features, "status": status}
}

// Register mounts only device-scoped business routes. The caller supplies JWT middleware.
// Installation users are trusted operators sharing this fleet, as in the original app.
func (m *Manager) Register(api *echo.Group) {
	api.GET("/devices", func(c *echo.Context) error {
		devices := []Device{}
		if err := m.DB.WithContext(c.Request().Context()).Order("created_at").Find(&devices).Error; err != nil {
			return apiError(c, err)
		}
		result := make([]map[string]any, 0, len(devices))
		for _, d := range devices {
			result = append(result, deviceView(d))
		}
		return c.JSON(http.StatusOK, result)
	})
	api.GET("/devices/server-public-key", func(c *echo.Context) error {
		key, err := m.PublicKey()
		if err != nil {
			return apiError(c, err)
		}
		return c.JSON(http.StatusOK, map[string]any{"keyId": m.Config.MQTT.KeyID, "publicKey": string(key)})
	})
	api.POST("/devices/enroll", func(c *echo.Context) error {
		var req struct {
			DeviceID string `json:"deviceId"`
		}
		if err := c.Bind(&req); err != nil {
			return apiError(c, err)
		}
		token, err := m.Provision(c.Request().Context(), req.DeviceID)
		if err != nil {
			return apiError(c, err)
		}
		key, err := m.PublicKey()
		if err != nil {
			return apiError(c, err)
		}
		c.Response().Header().Set("Cache-Control", "no-store")
		return c.JSON(http.StatusCreated, map[string]any{"deviceId": req.DeviceID, "registrationToken": token, "keyId": m.Config.MQTT.KeyID, "publicKey": string(key), "topicPrefix": m.Config.MQTT.TopicPrefix})
	})
	api.POST("/device-enrollments/:deviceID/revoke", func(c *echo.Context) error {
		if !validID.MatchString(c.Param("deviceID")) {
			return apiError(c, errors.New("invalid device ID"))
		}
		if err := m.Revoke(c.Request().Context(), c.Param("deviceID")); err != nil {
			return apiError(c, err)
		}
		return c.NoContent(http.StatusNoContent)
	})
	d := api.Group("/devices/:deviceID")
	d.Use(func(next echo.HandlerFunc) echo.HandlerFunc {
		return func(c *echo.Context) error {
			id := c.Param("deviceID")
			if !validID.MatchString(id) {
				return apiError(c, errors.New("invalid device ID"))
			}
			device, err := m.Device(c.Request().Context(), id)
			if err != nil {
				return apiError(c, err)
			}
			c.Set("fleet_device", device)
			return next(c)
		}
	})
	d.GET("", func(c *echo.Context) error { return c.JSON(http.StatusOK, deviceView(c.Get("fleet_device").(Device))) })
	d.PUT("", func(c *echo.Context) error {
		var req struct {
			Name string `json:"name"`
		}
		if err := c.Bind(&req); err != nil {
			return apiError(c, err)
		}
		if len(req.Name) < 1 || len(req.Name) > 128 {
			return apiError(c, errors.New("name must be 1..128 bytes"))
		}
		if err := m.DB.WithContext(c.Request().Context()).Model(&Device{}).Where("id = ?", c.Param("deviceID")).Update("name", req.Name).Error; err != nil {
			return apiError(c, err)
		}
		return c.NoContent(http.StatusNoContent)
	})
	d.GET("/status", m.statusHTTP)
	d.GET("/serial/status", m.statusHTTP)
	d.POST("/commands", m.commandHTTP)
	d.GET("/commands", func(c *echo.Context) error {
		list := []Command{}
		if err := m.DB.Where("device_id = ?", c.Param("deviceID")).Order("created_at DESC").Limit(200).Find(&list).Error; err != nil {
			return apiError(c, err)
		}
		return c.JSON(http.StatusOK, list)
	})
	d.GET("/commands/:id", func(c *echo.Context) error {
		var cmd Command
		if err := m.DB.First(&cmd, "id = ? AND device_id = ?", c.Param("id"), c.Param("deviceID")).Error; err != nil {
			return apiError(c, err)
		}
		return c.JSON(http.StatusOK, cmd)
	})
	for path, action := range map[string]string{"/serial/sms": "send_sms", "/serial/flymode": "set_flymode", "/serial/reboot": "reboot_mcu", "/serial/reset-stack": "reset_stack", "/locate": "set_locate"} {
		d.POST(path, func(c *echo.Context) error {
			args := map[string]any{}
			if c.Request().ContentLength != 0 {
				if err := c.Bind(&args); err != nil {
					return apiError(c, err)
				}
			}
			cmd, err := m.Command(c.Request().Context(), c.Param("deviceID"), action, args, "")
			if err != nil {
				return apiError(c, err)
			}
			return c.JSON(http.StatusAccepted, cmd)
		})
	}
	d.GET("/messages/stats", m.statsHTTP)
	d.GET("/messages/conversations", m.conversationsHTTP)
	d.GET("/messages/conversations/:peer/messages", m.conversationHTTP)
	d.DELETE("/messages/conversations/:peer", m.deleteConversationHTTP)
	d.DELETE("/messages/:id", func(c *echo.Context) error {
		r := m.DB.Where("device_id = ? AND id = ?", c.Param("deviceID"), c.Param("id")).Delete(&models.TextMessage{})
		if r.Error != nil {
			return apiError(c, r.Error)
		}
		if r.RowsAffected == 0 {
			return apiError(c, gorm.ErrRecordNotFound)
		}
		return c.NoContent(http.StatusNoContent)
	})
	d.DELETE("/messages", func(c *echo.Context) error {
		if err := m.DB.Where("device_id = ?", c.Param("deviceID")).Delete(&models.TextMessage{}).Error; err != nil {
			return apiError(c, err)
		}
		return c.NoContent(http.StatusNoContent)
	})
	d.GET("/properties/:id", func(c *echo.Context) error {
		s, err := m.GetSetting(c.Request().Context(), c.Param("deviceID"), c.Param("id"))
		if err != nil {
			return apiError(c, err)
		}
		var value any
		_ = json.Unmarshal([]byte(s.Value), &value)
		return c.JSON(http.StatusOK, map[string]any{"id": s.ID, "name": s.Name, "value": value})
	})
	d.PUT("/properties/:id", func(c *echo.Context) error {
		var req struct {
			Name  string          `json:"name"`
			Value json.RawMessage `json:"value"`
		}
		if err := c.Bind(&req); err != nil {
			return apiError(c, err)
		}
		if err := m.SetSetting(c.Request().Context(), c.Param("deviceID"), c.Param("id"), req.Name, req.Value); err != nil {
			return apiError(c, err)
		}
		return c.NoContent(http.StatusNoContent)
	})
	d.POST("/notifications/:type/test", func(c *echo.Context) error {
		if err := m.TestNotification(c.Request().Context(), c.Param("deviceID"), c.Param("type")); err != nil {
			return apiError(c, err)
		}
		return c.NoContent(http.StatusNoContent)
	})
	d.GET("/notification-deliveries", func(c *echo.Context) error {
		var rows []struct {
			ID        string `json:"id"`
			Attempts  int    `json:"attempts"`
			Done      bool   `json:"done"`
			LastError string `json:"lastError"`
			CreatedAt int64  `json:"createdAt"`
		}
		if err := m.DB.Model(&Delivery{}).Select("id, attempts, done, last_error, created_at").Where("device_id = ?", c.Param("deviceID")).Order("created_at DESC").Limit(100).Scan(&rows).Error; err != nil {
			return apiError(c, err)
		}
		return c.JSON(http.StatusOK, rows)
	})
	d.GET("/scheduled-tasks", func(c *echo.Context) error {
		tasks := []models.ScheduledTask{}
		if err := m.DB.Where("device_id = ?", c.Param("deviceID")).Find(&tasks).Error; err != nil {
			return apiError(c, err)
		}
		return c.JSON(http.StatusOK, tasks)
	})
	d.GET("/scheduled-tasks/:id", func(c *echo.Context) error {
		var task models.ScheduledTask
		if err := m.DB.First(&task, "id = ? AND device_id = ?", c.Param("id"), c.Param("deviceID")).Error; err != nil {
			return apiError(c, err)
		}
		return c.JSON(http.StatusOK, task)
	})
	d.POST("/scheduled-tasks", m.saveTaskHTTP)
	d.PUT("/scheduled-tasks/:id", m.saveTaskHTTP)
	d.DELETE("/scheduled-tasks/:id", func(c *echo.Context) error {
		r := m.DB.Where("id = ? AND device_id = ?", c.Param("id"), c.Param("deviceID")).Delete(&models.ScheduledTask{})
		if r.Error != nil {
			return apiError(c, r.Error)
		}
		if r.RowsAffected == 0 {
			return apiError(c, gorm.ErrRecordNotFound)
		}
		return c.NoContent(http.StatusNoContent)
	})
	d.POST("/scheduled-tasks/:id/trigger", func(c *echo.Context) error {
		cmd, err := m.TriggerTask(c.Request().Context(), c.Param("deviceID"), c.Param("id"))
		if err != nil {
			return apiError(c, err)
		}
		return c.JSON(http.StatusAccepted, cmd)
	})
}

func (m *Manager) statusHTTP(c *echo.Context) error {
	d := c.Get("fleet_device").(Device)
	status := map[string]any{}
	_ = json.Unmarshal([]byte(d.Status), &status)
	status["connected"] = d.Online
	status["port_name"] = d.Port
	status["deviceId"] = d.ID
	status["accessMode"] = d.AccessMode
	return c.JSON(http.StatusOK, status)
}
func (m *Manager) commandHTTP(c *echo.Context) error {
	args := map[string]any{}
	if err := c.Bind(&args); err != nil {
		return apiError(c, err)
	}
	action := stringValue(args["action"])
	cmd, err := m.Command(c.Request().Context(), c.Param("deviceID"), action, args, "")
	if err != nil {
		return apiError(c, err)
	}
	return c.JSON(http.StatusAccepted, cmd)
}
func (m *Manager) statsHTTP(c *echo.Context) error {
	id := c.Param("deviceID")
	stats := service.Stats{}
	queries := []struct {
		target *int64
		where  string
		args   []any
	}{{&stats.TotalCount, "", nil}, {&stats.IncomingCount, "type = ?", []any{"incoming"}}, {&stats.OutgoingCount, "type = ?", []any{"outgoing"}}}
	for _, q := range queries {
		db := m.DB.Model(&models.TextMessage{}).Where("device_id = ?", id)
		if q.where != "" {
			db = db.Where(q.where, q.args...)
		}
		if err := db.Count(q.target).Error; err != nil {
			return apiError(c, err)
		}
	}
	now := m.now()
	start := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location()).UnixMilli()
	if err := m.DB.Model(&models.TextMessage{}).Where("device_id = ? AND created_at >= ?", id, start).Count(&stats.TodayCount).Error; err != nil {
		return apiError(c, err)
	}
	return c.JSON(http.StatusOK, stats)
}
func (m *Manager) conversationsHTTP(c *echo.Context) error {
	var messages []models.TextMessage
	if err := m.DB.Where("device_id = ?", c.Param("deviceID")).Order("created_at DESC").Find(&messages).Error; err != nil {
		return apiError(c, err)
	}
	groups := map[string]*service.Conversation{}
	for i := range messages {
		msg := &messages[i]
		peer := msg.To
		if msg.Type == models.MessageTypeIncoming {
			peer = msg.From
		}
		if peer == "" {
			continue
		}
		if groups[peer] == nil {
			groups[peer] = &service.Conversation{Peer: peer, LastMessage: msg}
		}
		groups[peer].MessageCount++
	}
	result := make([]*service.Conversation, 0, len(groups))
	for _, v := range groups {
		result = append(result, v)
	}
	sort.Slice(result, func(i, j int) bool { return result[i].LastMessage.CreatedAt > result[j].LastMessage.CreatedAt })
	return c.JSON(http.StatusOK, result)
}

// Echo's default router matches RawPath when present, otherwise the already
// decoded Path. Decode only the former, preserving literal '+' and '%' values.
func conversationPeer(c *echo.Context) (string, error) {
	if c.Request().URL.RawPath != "" {
		return url.PathUnescape(c.Param("peer"))
	}
	return c.Param("peer"), nil
}

func (m *Manager) conversationHTTP(c *echo.Context) error {
	peer, err := conversationPeer(c)
	if err != nil {
		return apiError(c, err)
	}
	list := []models.TextMessage{}
	if err := m.DB.Where("device_id = ? AND ((type = ? AND \"from\" = ?) OR (type = ? AND \"to\" = ?))", c.Param("deviceID"), "incoming", peer, "outgoing", peer).Order("created_at").Find(&list).Error; err != nil {
		return apiError(c, err)
	}
	return c.JSON(http.StatusOK, list)
}
func (m *Manager) deleteConversationHTTP(c *echo.Context) error {
	peer, err := conversationPeer(c)
	if err != nil {
		return apiError(c, err)
	}
	if err := m.DB.Where("device_id = ? AND ((type = ? AND \"from\" = ?) OR (type = ? AND \"to\" = ?))", c.Param("deviceID"), "incoming", peer, "outgoing", peer).Delete(&models.TextMessage{}).Error; err != nil {
		return apiError(c, err)
	}
	return c.NoContent(http.StatusNoContent)
}
func (m *Manager) saveTaskHTTP(c *echo.Context) error {
	var input struct {
		Name         string `json:"name"`
		Enabled      bool   `json:"enabled"`
		IntervalDays int    `json:"intervalDays"`
		PhoneNumber  string `json:"phoneNumber"`
		Content      string `json:"content"`
	}
	if err := c.Bind(&input); err != nil {
		return apiError(c, err)
	}
	if input.Name == "" || len(input.Name) > 128 || input.IntervalDays < 1 || input.IntervalDays > 3650 || !smsValid(input.PhoneNumber, input.Content) {
		return apiError(c, errors.New("invalid scheduled task"))
	}
	id := c.Param("deviceID")
	taskID := c.Param("id")
	if taskID != "" {
		var task models.ScheduledTask
		if err := m.DB.First(&task, "id = ? AND device_id = ?", taskID, id).Error; err != nil {
			return apiError(c, err)
		}
		task.Name = input.Name
		task.Enabled = input.Enabled
		task.IntervalDays = input.IntervalDays
		task.PhoneNumber = input.PhoneNumber
		task.Content = input.Content
		if err := m.DB.Save(&task).Error; err != nil {
			return apiError(c, err)
		}
		return c.JSON(http.StatusOK, task)
	}
	task := models.ScheduledTask{ID: uuid.NewString(), DeviceID: id, Name: input.Name, Enabled: input.Enabled, IntervalDays: input.IntervalDays, PhoneNumber: input.PhoneNumber, Content: input.Content}
	if err := m.DB.Create(&task).Error; err != nil {
		return apiError(c, err)
	}
	return c.JSON(http.StatusCreated, task)
}
