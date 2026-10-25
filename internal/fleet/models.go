package fleet

// These tables are initialized for a new installation; no legacy data import.
type Device struct {
	ID          string `gorm:"primaryKey;size:64" json:"id"`
	Name        string `json:"name"`
	AccessMode  string `json:"accessMode"`
	Online      bool   `json:"online"`
	Port        string `json:"port"`
	Features    string `gorm:"type:text" json:"-"`
	Status      string `gorm:"type:text" json:"-"`
	ActiveSID   string `gorm:"column:active_sid" json:"-"`
	LastSeen    int64  `json:"lastSeen"`
	LastSMSAt   int64  `json:"lastSmsAt"`
	AutoFlymode bool   `json:"-"`
	CreatedAt   int64  `gorm:"autoCreateTime:milli" json:"createdAt"`
	UpdatedAt   int64  `gorm:"autoUpdateTime:milli" json:"updatedAt"`
}

type Credential struct {
	DeviceID  string `gorm:"primaryKey;size:64" json:"deviceId"`
	TokenHash string `json:"-"`
	Revoked   bool   `json:"revoked"`
	CreatedAt int64  `gorm:"autoCreateTime:milli" json:"createdAt"`
}

type Session struct {
	AADMode    string `gorm:"column:aad_mode"`
	ID         string `gorm:"primaryKey;size:32"`
	DeviceID   string `gorm:"index"`
	WrappedKey string `gorm:"type:text"`
	KeyID      string
	UpSeq      uint64
	DownSeq    uint64
	Retired    bool
	CreatedAt  int64 `gorm:"autoCreateTime:milli"`
}

type Command struct {
	ID             string `gorm:"primaryKey;size:64" json:"id"`
	DeviceID       string `gorm:"index;not null" json:"deviceId"`
	Action         string `json:"action"`
	Status         string `gorm:"index" json:"status"`
	Payload        string `gorm:"type:text" json:"-"`
	Result         string `gorm:"type:text" json:"result"`
	ExpiresAt      int64  `json:"expiresAt"`
	TaskID         string `json:"taskId,omitempty"`
	RestoreFlymode bool   `json:"-"`
	RestoreAfter   int64  `json:"-"`
	CreatedAt      int64  `gorm:"autoCreateTime:milli" json:"createdAt"`
	UpdatedAt      int64  `gorm:"autoUpdateTime:milli" json:"updatedAt"`
}

type Setting struct {
	DeviceID string `gorm:"primaryKey;size:64" json:"deviceId"`
	ID       string `gorm:"primaryKey;size:64" json:"id"`
	Name     string `json:"name"`
	Value    string `gorm:"type:text" json:"value"`
}

// Composite primary key makes retransmitted events idempotent across sessions.
type Receipt struct {
	DeviceID  string `gorm:"primaryKey;size:64"`
	MessageID string `gorm:"primaryKey;size:128"`
	CreatedAt int64  `gorm:"autoCreateTime:milli"`
}

type Delivery struct {
	ID          string `gorm:"primaryKey"`
	DeviceID    string `gorm:"index"`
	Payload     string `gorm:"type:text"`
	Attempts    int
	NextAttempt int64 `gorm:"index"`
	Done        bool  `gorm:"index"`
	LastError   string
	CreatedAt   int64 `gorm:"autoCreateTime:milli"`
}
