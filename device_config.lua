-- Luatools: burn main.lua and all device_*.lua files together.
return {
    access_mode = "serial", -- "serial" or "mqtt"; effective after reboot
    device_id = "", -- empty: use IMEI; use only letters, digits, _ and -
    device_name = "Air780",
    phone_number = "", -- SIM 未保存本机号码时用于展示，如 "13250574785"
    usb_net = true, -- 开机自动开启 RNDIS 4G 共享（会让串口短暂断开重连）
    uart_id = uart.VUART_0,
    heartbeat_ms = 60000,
    debug_sms_content = true, -- prints plaintext SMS to local debug log
    mqtt = {
        dns_log = true, -- log cellular DNS answers before MQTT connect (up to 5s)
        host = "", -- reachable host/IP, NOT the Docker-only name sms_mqtt
        port = 1883,
        username = "",
        password = "",
        tls = false,
        ca_file = "/luadb/mqtt_ca.crt", -- required when tls=true
        topic_prefix = "sms/v1/devices",
        keepalive = 120,
        retry_ms = 5000,
        registration_timeout_ms = 30000,
        max_packet_bytes = 32768,
        max_queue = 100,
        max_command_queue = 20,
        ack_timeout_ms = 15000,
        max_command_ttl = 300,
        public_key = "", -- inline PEM public key, takes precedence over file
        public_key_file = "/luadb/server_public.pem",
        key_id = "server-key-1",
        registration_token = "", -- independent per-device secret from backend
    }
}
