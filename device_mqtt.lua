local secure = require "device_crypto"
local M = {}
local cfg, device_id, info_fn, command_fn
local client, session, connected, subscribed, authenticated
local outbox, inbox = {}, {}
local event_counter = 0
local server_epoch, server_uptime

local function uptime()
    local high, low = mcu.ticks2(2)
    return high * 1000000 + low
end

function M.now()
    if not authenticated or not server_epoch then return nil end
    return server_epoch + uptime() - server_uptime
end

local function topic(suffix)
    return cfg.topic_prefix .. "/" .. device_id .. "/" .. suffix
end

function M.send(data)
    if #outbox >= cfg.max_queue then
        log.error("MQTT", "outbox full; event not accepted", data.type)
        return false
    end
    event_counter = event_counter + 1
    local id = device_id .. "-" .. secure.hex(crypto.trng(12)) .. "-" .. event_counter
    -- Encode now so later mutations cannot change an outstanding event.
    local body = assert(json.encode({message_id = id, data = data}))
    if #body * 2 + 1024 > cfg.max_packet_bytes then
        log.error("MQTT", "event exceeds configured packet size", data.type)
        return false
    end
    local suffix = (data.type == "heartbeat" or data.type == "status_response" or data.type == "system_ready")
        and "status" or ((data.type == "cmd_response" or data.type == "sms_send_result" or data.type == "error")
        and "results" or "events")
    outbox[#outbox + 1] = {id = id, body = body, suffix = suffix}
    return true
end

local function on_event(c, event, data, payload)
    if c ~= client then return end
    if event == "conack" then
        connected = true
        if not c:subscribe(topic("down"), 1) then connected = false end
    elseif event == "suback" then
        if data == true then subscribed = true else connected = false end
    elseif event == "disconnect" or event == "error" then
        connected = false
    elseif event == "recv" and data == topic("down") then
        if type(payload) == "string" and #payload <= cfg.max_packet_bytes and #inbox < cfg.max_command_queue then
            inbox[#inbox + 1] = payload
        else
            log.warn("MQTT", "incoming queue/packet limit")
        end
    end
end

local function close()
    connected, subscribed, authenticated = false, false, false
    local old = client
    client, session, inbox = nil, nil, {}
    if old then pcall(function() old:close() end) end
end

local function process_inbox()
    while #inbox > 0 do
        local ok, kind, body = pcall(secure.open, session, table.remove(inbox, 1))
        if not ok then
            log.warn("MQTT", "rejected invalid encrypted packet")
        elseif kind == "register_result" and not authenticated then
            if body.accepted == true and type(body.server_time) == "number" and body.server_time > 1700000000 then
                server_epoch, server_uptime = math.floor(body.server_time), uptime()
                authenticated = true
                log.info("MQTT", "backend authenticated device", device_id)
            else
                log.warn("MQTT", "backend rejected registration")
                connected = false
            end
        elseif authenticated and kind == "ack" then
            if outbox[1] and body.message_id == outbox[1].id then table.remove(outbox, 1) end
        elseif authenticated and kind == "command" then
            local success = pcall(command_fn, body)
            if not success then log.error("MQTT", "command handler failed") end
        end
    end
end

-- Use the cellular resolver without opening a connection to the MQTT port.
-- remote_port=0 completes at DNS resolution; keep MQTT using the hostname for TLS.
local function log_dns()
    if cfg.dns_log == false then return end
    if not (socket.create and socket.config and socket.connect and socket.remoteIP
        and socket.close and socket.release) then
        log.warn("DNS", cfg.host, "diagnostic API unavailable")
        return
    end
    local ctrl
    local ok, err = pcall(function()
        local done, succeeded = false, false
        ctrl = assert(socket.create(socket.LWIP_GP, function(_, event, code)
            if event == socket.ON_LINE then
                done, succeeded = true, code == 0
            elseif event == socket.CLOSED or code == -1 then
                done, succeeded = true, false
            end
        end), "socket.create failed")
        assert(socket.config(ctrl), "socket.config failed")
        local started, completed = socket.connect(ctrl, cfg.host, 0, false)
        assert(started, "DNS request failed")
        if completed then done, succeeded = true, true end
        local waited = 0
        while not done and waited < 5000 do
            sys.wait(100)
            waited = waited + 100
        end
        assert(done and succeeded, done and "DNS lookup failed" or "DNS lookup timed out")
        local addresses = {}
        local ip1, ip2, ip3, ip4 = socket.remoteIP(ctrl)
        for _, ip in pairs({ip1, ip2, ip3, ip4}) do
            if type(ip) == "string" and ip ~= "" then addresses[#addresses + 1] = ip end
        end
        assert(#addresses > 0, "DNS returned no addresses")
        log.info("DNS", cfg.host, "resolved IPv4", table.concat(addresses, ", "), "MQTT port", cfg.port)
    end)
    if ctrl then
        pcall(socket.close, ctrl)
        pcall(socket.release, ctrl)
    end
    if not ok then log.warn("DNS", cfg.host, tostring(err), "continuing MQTT connection") end
end

local function connect_once()
    log_dns()
    session = secure.new(cfg, device_id) -- fresh keys on every reconnect
    local tls
    if cfg.tls then
        -- Correct clock is required for certificate validity checks.
        socket.sntp()
        assert(sys.waitUntil("NTP_UPDATE", 30000), "TLS time synchronization timed out")
        local ca = io.readFile(cfg.ca_file)
        assert(type(ca) == "string" and #ca > 0, "TLS CA file missing")
        tls = {server_cert = ca, verify = 2}
    end
    client = assert(mqtt.create(socket.LWIP_GP, cfg.host, cfg.port, tls, {rxSize = cfg.max_packet_bytes}),
        "mqtt.create failed")
    client:on(on_event)
    assert(client:auth("sms_" .. device_id, cfg.username, cfg.password, true), "mqtt.auth failed")
    client:keepalive(cfg.keepalive)
    -- Reconnection is owned by this task; do not enable a second reconnect loop.
    assert(client:connect(), "mqtt.connect failed")
    local waited = 0
    while not subscribed and waited < cfg.registration_timeout_ms do
        sys.wait(100)
        waited = waited + 100
    end
    assert(connected and subscribed, "MQTT connect/subscribe timed out")
    local registration = secure.seal(session, "register", {
        registration_token = cfg.registration_token, key_id = cfg.key_id, features = info_fn(),
    })
    assert(#registration <= cfg.max_packet_bytes, "registration too large")
    assert(client:publish(topic("register"), registration, 1, 0), "registration publish failed")
    waited = 0
    while connected and not authenticated and waited < cfg.registration_timeout_ms do
        process_inbox()
        sys.wait(100)
        waited = waited + 100
    end
    assert(authenticated, "backend authentication timed out or rejected")

    local pending, remaining = nil, 0
    while connected do
        process_inbox()
        local item = outbox[1]
        if item and (pending ~= item.id or remaining <= 0) then
            -- Retries keep message_id but use a fresh encryption sequence/IV.
            local packet = secure.seal(session, "event", json.decode(item.body))
            assert(client:publish(topic(item.suffix), packet, 1, 0), "event publish failed")
            pending, remaining = item.id, cfg.ack_timeout_ms
        end
        sys.wait(100)
        remaining = remaining - 100
    end
end

function M.start(config, id, get_info, process_command)
    cfg, device_id, info_fn, command_fn = config, id, get_info, process_command
    assert(type(cfg.host) == "string" and cfg.host ~= "", "configure MQTT host")
    assert(type(cfg.username) == "string" and cfg.username ~= "" and type(cfg.password) == "string"
        and cfg.password ~= "", "configure MQTT username/password")
    assert(type(cfg.registration_token) == "string" and #cfg.registration_token >= 32,
        "configure independent registration token (32+ characters)")
    assert(type(cfg.port) == "number" and cfg.port >= 1 and cfg.port <= 65535, "invalid MQTT port")
    assert(type(cfg.tls) == "boolean", "tls must be boolean")
    assert(type(cfg.topic_prefix) == "string" and cfg.topic_prefix:match("^[%w_/-]+$"), "invalid topic prefix")
    sys.taskInit(function()
        while true do
            while not socket.adapter(socket.LWIP_GP) do sys.waitUntil("IP_READY", 1000) end
            log.info("MQTT", "connecting", cfg.host, cfg.port, "TLS", cfg.tls)
            local ok, err = pcall(connect_once)
            if not ok then log.warn("MQTT", "connection/registration failed", tostring(err)) end
            close()
            sys.wait(cfg.retry_ms)
        end
    end)
end

return M
