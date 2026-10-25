package config

type AppConfig struct {
	JWT    JWTConfig         `json:"JWT"`
	Users  map[string]string `json:"Users"`  // 用户名 -> bcrypt加密的密码
	Serial SerialConfig      `json:"Serial"` // 串口配置
	OIDC   *OIDCConfig       `json:"OIDC"`   // OIDC配置（可选）
	MQTT   MQTTConfig        `json:"MQTT"`
}

// JWTConfig JWT配置
type JWTConfig struct {
	Secret       string `json:"Secret"`
	ExpiresHours int    `json:"ExpiresHours"`
}

// SerialConfig 串口配置
type SerialConfig struct {
	Port                string   `json:"Port"` // 串口路径，为空则自动检测
	Disabled            bool     `json:"Disabled"`
	Ports               []string `json:"Ports"`
	ScanIntervalSeconds int      `json:"ScanIntervalSeconds"`
}

type MQTTConfig struct {
	Enabled        bool   `json:"Enabled"`
	Broker         string `json:"Broker"`
	Username       string `json:"Username"`
	Password       string `json:"Password"`
	ClientID       string `json:"ClientID"`
	TLS            bool   `json:"TLS"`
	CAFile         string `json:"CAFile"`
	TopicPrefix    string `json:"TopicPrefix"`
	PrivateKeyFile string `json:"PrivateKeyFile"`
	KeyID          string `json:"KeyID"`
	OfflineSeconds int    `json:"OfflineSeconds"`
}

// OIDCConfig OIDC认证配置
type OIDCConfig struct {
	Enabled      bool   `json:"Enabled"`      // 是否启用OIDC
	Issuer       string `json:"Issuer"`       // OIDC Provider的Issuer URL
	ClientID     string `json:"ClientID"`     // Client ID
	ClientSecret string `json:"ClientSecret"` // Client Secret
	RedirectURL  string `json:"RedirectURL"`  // 回调URL
}
