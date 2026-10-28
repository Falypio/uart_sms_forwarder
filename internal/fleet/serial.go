package fleet

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"go.bug.st/serial"
	"go.bug.st/serial/enumerator"
	"go.uber.org/zap"
	"gorm.io/gorm"
)

type serialPort interface {
	io.ReadWriteCloser
	SetReadTimeout(time.Duration) error
}
type serialPeer struct {
	m        *Manager
	id, path string
	port     serialPort
	mu       sync.Mutex
	once     sync.Once
}

func (p *serialPeer) Close() { p.once.Do(func() { _ = p.port.Close() }) }
func (p *serialPeer) Send(ctx context.Context, cmd map[string]any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	b, err := json.Marshal(cmd)
	if err != nil {
		return err
	}
	// Lua's legacy command framing searches a delimiter. Escape it inside JSON strings.
	b = []byte(strings.ReplaceAll(string(b), ":CMD_END", `\u003aCMD_END`))
	p.mu.Lock()
	defer p.mu.Unlock()
	return writePort(p.port, append(append([]byte("CMD_START:"), b...), []byte(":CMD_END\r\n")...))
}
func writePort(w io.Writer, b []byte) error {
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n <= 0 || n > len(b) {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}

// Frames are bounded and assembled across reads, including the probe response.
type frameBuffer struct{ data string }

func (f *frameBuffer) feed(chunk []byte) ([]json.RawMessage, error) {
	f.data += string(chunk)
	var frames []json.RawMessage
	for {
		start := strings.Index(f.data, "SMS_START:")
		if start < 0 {
			if len(f.data) > 65536 {
				return nil, errors.New("serial frame overflow")
			}
			break
		}
		payload := f.data[start+10:]
		decoder := json.NewDecoder(strings.NewReader(payload))
		var value json.RawMessage
		decodeErr := decoder.Decode(&value)
		if decodeErr != nil {
			if len(f.data) > 65536 {
				return nil, errors.New("serial frame overflow")
			}
			if errors.Is(decodeErr, io.EOF) || errors.Is(decodeErr, io.ErrUnexpectedEOF) {
				break
			}
			return nil, errors.New("invalid serial JSON frame")
		}
		end := start + 10 + int(decoder.InputOffset())
		if len(f.data) < end+8 {
			break
		}
		if f.data[end:end+8] != ":SMS_END" {
			return nil, errors.New("invalid serial frame suffix")
		}
		raw := f.data[start+10 : end]
		f.data = f.data[end+8:]
		if len(raw) > 32768 || !json.Valid([]byte(raw)) {
			return nil, errors.New("invalid serial JSON frame")
		}
		frames = append(frames, json.RawMessage(raw))
	}
	return frames, nil
}

func serialIdentity(raw json.RawMessage) (string, map[string]any, error) {
	var event map[string]any
	if json.Unmarshal(raw, &event) != nil || stringValue(event["type"]) != "status_response" {
		return "", nil, errors.New("not identity response")
	}
	id := stringValue(event["device_id"])
	if !validID.MatchString(id) {
		return "", nil, errors.New("device must report stable device_id")
	}
	if stringValue(event["access_mode"]) != "serial" {
		return "", nil, errors.New("device not in serial mode")
	}
	return id, event, nil
}

// autoDetectPorts enumerates candidate serial ports when neither Port nor Ports
// is configured. Only USB-backed ports are returned: the device is always a USB
// CDC device, while virtual ports (Bluetooth links, legacy COM) can never carry
// the SMS protocol and would each burn a handshake timeout on every scan.
// Returning a USB port list also means the device keeps working after it is
// moved to a different USB socket, where Windows assigns new COM numbers.
func (m *Manager) autoDetectPorts() []string {
	details, err := enumerator.GetDetailedPortsList()
	if err != nil {
		m.log.Warn("serial enumeration failed", zap.Error(err))
		return nil
	}
	ports := make([]string, 0, len(details))
	for _, d := range details {
		if !d.IsUSB {
			continue
		}
		ports = append(ports, d.Name)
	}
	m.log.Debug("serial auto-detect", zap.Strings("ports", ports))
	return ports
}

func (m *Manager) scanSerial(ctx context.Context) {
	ticker := time.NewTicker(time.Duration(m.Config.Serial.ScanIntervalSeconds) * time.Second)
	defer ticker.Stop()
	for {
		ports := m.Config.Serial.Ports
		if len(ports) == 0 && m.Config.Serial.Port != "" {
			ports = []string{m.Config.Serial.Port}
		}
		if len(ports) == 0 {
			ports = m.autoDetectPorts()
		}
		for _, path := range ports {
			m.mu.Lock()
			busy := m.ports[path]
			if !busy {
				m.ports[path] = true
			}
			m.mu.Unlock()
			if !busy {
				go func(path string) {
					defer func() { m.mu.Lock(); delete(m.ports, path); m.mu.Unlock() }()
					port, err := serial.Open(path, &serial.Mode{BaudRate: 115200, DataBits: 8, Parity: serial.NoParity, StopBits: serial.OneStopBit})
					if err != nil {
						return
					}
					if err := m.serveSerial(ctx, path, port); err != nil {
						m.log.Debug("serial probe/connection ended", zap.String("port", path), zap.Error(err))
					}
				}(path)
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

func (m *Manager) serveSerial(ctx context.Context, path string, port serialPort) error {
	p := &serialPeer{m: m, path: path, port: port}
	defer p.Close()
	if err := port.SetReadTimeout(500 * time.Millisecond); err != nil {
		return err
	}
	// Only a status query is used for probing; no SMS/reset/flymode side effects.
	if err := p.Send(ctx, map[string]any{"action": "get_status", "request_id": uuid.NewString()}); err != nil {
		return err
	}
	frames := &frameBuffer{}
	buffer := make([]byte, 4096)
	deadline := time.Now().Add(8 * time.Second)
	lastProbe := time.Now()
	var initial json.RawMessage
	var pending []json.RawMessage
	for time.Now().Before(deadline) && ctx.Err() == nil {
		n, err := port.Read(buffer)
		if err != nil {
			return err
		}
		packets, err := frames.feed(buffer[:n])
		if err != nil {
			return err
		}
		for _, raw := range packets {
			if id, _, err := serialIdentity(raw); p.id == "" && err == nil {
				p.id = id
				initial = raw
			} else {
				pending = append(pending, raw)
			}
		}
		if p.id != "" {
			break
		}
		if len(pending) > 100 {
			return errors.New("too many frames before identity")
		}
		if time.Since(lastProbe) >= 2*time.Second {
			if err := p.Send(ctx, map[string]any{"action": "get_status", "request_id": uuid.NewString()}); err != nil {
				return err
			}
			lastProbe = time.Now()
		}
	}
	if p.id == "" {
		return errors.New("serial identity handshake timed out")
	}
	if err := m.attach(p.id, "serial", p); err != nil {
		return err
	}
	defer func() {
		m.mu.Lock()
		current, ok := m.connections[p.id]
		if ok && current.transport == p {
			delete(m.connections, p.id)
		}
		m.mu.Unlock()
		if ok && current.transport == p {
			_ = m.DB.Model(&Device{}).Where("id = ? AND access_mode = ? AND port = ?", p.id, "serial", path).Update("online", false).Error
		}
	}()
	var e map[string]any
	_ = json.Unmarshal(initial, &e)
	features, _ := json.Marshal(e["features"])
	// Serialize switching from an old MQTT session into a physically trusted USB session.
	m.mqttMu.Lock()
	err := m.DB.Transaction(func(tx *gorm.DB) error {
		var d Device
		err := tx.First(&d, "id = ?", p.id).Error
		if errors.Is(err, gorm.ErrRecordNotFound) {
			d = Device{ID: p.id, Name: p.id, LastSMSAt: m.now().UnixMilli()}
		} else if err != nil {
			return err
		}
		d.AccessMode = "serial"
		d.Port = path
		d.Online = true
		d.ActiveSID = ""
		d.Features = string(features)
		d.Status = mergeStatus(d.Status, e)
		d.LastSeen = m.now().UnixMilli()
		if err := tx.Model(&Session{}).Where("device_id = ?", p.id).Update("retired", true).Error; err != nil {
			return err
		}
		return tx.Save(&d).Error
	})
	m.mqttMu.Unlock()
	if err != nil {
		return err
	}
	m.log.Info("serial device connected", zap.String("device_id", p.id), zap.String("port", path))
	for _, raw := range pending {
		var e map[string]any
		if json.Unmarshal(raw, &e) != nil || stringValue(e["device_id"]) != p.id {
			continue
		}
		if err := m.DB.Transaction(func(tx *gorm.DB) error { return m.eventTx(tx, p.id, "", raw) }); err != nil {
			return err
		}
	}
	lastPoll := time.Now()
	lastReceive := time.Now()
	for ctx.Err() == nil {
		n, err := port.Read(buffer)
		if err != nil {
			return err
		}
		packets, err := frames.feed(buffer[:n])
		if err != nil {
			return err
		}
		for _, raw := range packets {
			var event map[string]any
			if json.Unmarshal(raw, &event) != nil {
				return errors.New("invalid serial event")
			}
			if stringValue(event["device_id"]) != p.id {
				return errors.New("serial identity changed")
			}
			lastReceive = time.Now()
			if err := m.DB.Transaction(func(tx *gorm.DB) error { return m.eventTx(tx, p.id, "", raw) }); err != nil {
				m.log.Warn("serial event not saved", zap.String("device_id", p.id), zap.Error(err))
			}
		}
		if time.Since(lastReceive) > 180*time.Second {
			return errors.New("serial heartbeat timed out")
		}
		if time.Since(lastPoll) >= 10*time.Second {
			if err := p.Send(ctx, map[string]any{"action": "get_status", "device_id": p.id}); err != nil {
				return err
			}
			lastPoll = time.Now()
		}
	}
	return ctx.Err()
}
