PROJECT = "uart_sms_forwarder"
VERSION = "2.1.1"
sys = require "sys"
require "sysplus"
local config = require "device_config"
assert(config.access_mode == "serial" or config.access_mode == "mqtt", "invalid access_mode")
local mqtt_mode = config.access_mode == "mqtt"
local transport, journal
local device_id = config.device_id
if not device_id or device_id == "" then device_id = mobile.imei() end
assert(type(device_id) == "string" and #device_id > 0 and #device_id <= 64
    and device_id:match("^[%w_-]+$"), "configure a valid device_id or check IMEI")
local uartid = config.uart_id
local send_queue, msg_buffer, uart_buffer = {}, {}, ""
local max_send_queue, max_msg_buffer = 10, 50
local call_ring_count, sms_ready = 0, false
local deferred = {}
local flymode_restore_timer
local locating, locate_timer, locate_output = false, nil, nil
-- GPIO21 is dedicated to the board's active-high locator LED; start switched off.
if gpio and gpio.setup then
    local ok, output = pcall(gpio.setup, 21, 0)
    if ok and type(output) == "function" then locate_output = output
    else log.warn("LOCATE", "GPIO21 unavailable") end
end

local function set_locating(enabled)
    assert(locate_output, "GPIO21 locator unavailable")
    if locating == enabled then return end
    if locate_timer then sys.timerStop(locate_timer); locate_timer = nil end
    locating = false
    locate_output(0)
    if enabled then
        local level = 1
        locate_output(level)
        locate_timer = assert(sys.timerLoopStart(function()
            level = 1 - level
            locate_output(level)
        end, 500), "locator timer unavailable")
        locating = true
    end
    log.info("LOCATE", "GPIO21", locating and "blinking" or "off")
end

log.info("main", PROJECT, VERSION, device_id, config.access_mode)
-- No mobile.setAuto(0): it is NOT a mobile-data disable switch.
if wdt then
    wdt.init(9000)
    sys.timerLoopStart(wdt.feed, 3000)
end
if not mqtt_mode then uart.setup(uartid, 115200, 8, 1) end

local function read_phone_number()
    local number = mobile.number(0)
    if number and number ~= "" and number ~= "unknown" then
        log.info("phone_number", "本机手机号:", number)
        return number
    end
    log.warn("phone_number", "未读取到手机号，SIM卡可能未保存本机号码")
    return ""
end

local function uptime()
    local high, low = mcu.ticks2(2)
    return high * 1000000 + low
end

function get_mobile_info()
    local stat, iccid = mobile.status(), mobile.iccid()
    local csq = mobile.csq() or 0
    local level = (csq == 0 or csq == 99) and 0 or csq
    return {
        sim_ready = iccid ~= nil and iccid ~= "" and iccid ~= "unknown",
        sms_ready = sms_ready, iccid = iccid or "unknown", imsi = mobile.imsi() or "unknown",
        number = mobile.number(0) or "", csq = csq, rssi = mobile.rssi() or -113,
        rsrp = mobile.rsrp() or -140, rsrq = mobile.rsrq() or -20,
        signal_level = level, signal_desc = level == 0 and "无信号" or (level >= 20 and "强" or (level >= 10 and "中" or "弱")),
        is_registered = stat == 1 or stat == 5 or stat == 6 or stat == 7,
        is_roaming = stat == 5 or stat == 7, net_status = stat,
        uptime = uptime(), flymode = mobile.flymode(),
    }
end

local function features()
    return {device_id = device_id, name = config.device_name, imei = mobile.imei(),
        model = rtos.bsp(), firmware = rtos.version(), project = PROJECT, version = VERSION,
        access_mode = config.access_mode, locate_led = locate_output ~= nil, locate_gpio = 21, mobile = get_mobile_info()}
end

local function emit(data)
    data.device_id, data.access_mode = device_id, config.access_mode
    if data.timestamp == nil then data.timestamp = os.time() end
    if mqtt_mode then return transport.send(data) end
    local encoded = json.encode(data)
    if not encoded then return false end
    return uart.write(uartid, "SMS_START:" .. encoded .. ":SMS_END\r\n") ~= nil
end

local function bool(value)
    if value == true or value == 1 or value == "true" or value == "1" then return true end
    if value == false or value == 0 or value == "false" or value == "0" then return false end
    return nil
end

local function process_command(cmd, remote)
    if type(cmd) ~= "table" or type(cmd.action) ~= "string" then
        emit({type = "error", msg = "missing action"})
        return
    end
    if cmd.device_id and cmd.device_id ~= device_id then return end
    if remote and cmd.device_id ~= device_id then return end
    local action, enabled = cmd.action, bool(cmd.enabled)
    local function reject(message)
        emit({type = "error", request_id = cmd.request_id, action = action, msg = message})
    end
    if action == "send_sms" then
        if type(cmd.to) ~= "string" or not cmd.to:match("^%+?%d+$") or #cmd.to > 24
            or type(cmd.content) ~= "string" or #cmd.content == 0 or #cmd.content > 4096 then
            reject("invalid phone/content (max 4096 UTF-8 bytes)"); return
        end
        if #send_queue >= max_send_queue then reject("send queue full"); return end
    elseif action == "set_locate" then
        if enabled == nil then reject("invalid enabled value"); return end
        if not locate_output then reject("GPIO21 locator unavailable"); return end
    elseif action == "set_flymode" then
        if enabled == nil then reject("invalid enabled value"); return end
        if remote and enabled and (type(cmd.duration_seconds) ~= "number" or cmd.duration_seconds % 1 ~= 0
            or cmd.duration_seconds < 1 or cmd.duration_seconds > 86400) then
            reject("MQTT flymode requires duration_seconds (1..86400)"); return
        end
    elseif action ~= "get_status" and action ~= "reset_stack" and action ~= "reboot_mcu" then
        reject("unknown command"); return
    end
    if (action == "reset_stack" or action == "reboot_mcu" or action == "set_flymode") and #deferred >= 10 then
        reject("control queue full"); return
    end

    local slot
    if remote then
        local error_msg, previous
        slot, error_msg, previous = journal.begin(cmd)
        if not slot then
            if previous then emit(previous) else reject(error_msg) end
            return
        end
    end
    local request_id = cmd.request_id or tostring(os.time())
    local function reply(data)
        data.request_id = request_id
        if remote then journal.finish(slot, data) end
        emit(data)
    end
    if action == "send_sms" then
        send_queue[#send_queue + 1] = {request_id = request_id, to = cmd.to, content = cmd.content,
            slot = slot, expires_at = remote and cmd.expires_at or nil}
        sys.publish("SMS_SEND_QUEUE_CHANGED")
        emit({type = "cmd_response", action = action, request_id = request_id, result = "queued"})
    elseif action == "set_locate" then
        local ok, err = pcall(set_locating, enabled)
        if not ok then
            locating = false
            if locate_timer then sys.timerStop(locate_timer); locate_timer = nil end
            if locate_output then pcall(locate_output, 0) end
        end
        reply({type = "cmd_response", action = action, result = ok and "ok" or "error",
            locating = locating, gpio = 21, msg = not ok and tostring(err) or nil})
    elseif action == "get_status" then
        -- Status can be large; store only a small completion marker in fskv.
        if remote then journal.finish(slot, {type = "cmd_response", action = action, request_id = request_id, result = "ok"}) end
        emit({type = "status_response", request_id = request_id, version = VERSION,
            mem_kb = math.floor(collectgarbage("count")), locating = locating, mobile = get_mobile_info(), features = features()})
    else
        local job = {action = action, enabled = enabled, duration = cmd.duration_seconds,
            remote = remote, slot = slot, request_id = request_id, expires_at = remote and cmd.expires_at or nil}
        deferred[#deferred + 1] = job
        -- Accepted != completed. Disruptive operations execute in a task, not a UART/MQTT callback.
        emit({type = "cmd_response", action = action, request_id = request_id, result = "accepted"})
    end
end

function process_uart_command(cmd) process_command(cmd, false) end

if mqtt_mode then
    transport = require "device_mqtt"
    journal = require "device_commands"
    journal.init(config.mqtt, transport.now)
    transport.start(config.mqtt, device_id, features, function(cmd) process_command(cmd, true) end)
else
    uart.on(uartid, "receive", function(id, len)
        uart_buffer = uart_buffer .. (uart.read(id, len) or "")
        while true do
            local first = uart_buffer:find("CMD_START:", 1, true)
            if not first then break end
            local last = uart_buffer:find(":CMD_END", first + 10, true)
            if not last then break end
            local raw = uart_buffer:sub(first + 10, last - 1)
            uart_buffer = uart_buffer:sub(last + 8)
            if #raw <= 8192 then
                local ok, cmd = pcall(json.decode, raw)
                if ok and type(cmd) == "table" then process_uart_command(cmd)
                else emit({type = "error", msg = "Invalid JSON"}) end
            else emit({type = "error", msg = "Command too large"}) end
        end
        if #uart_buffer > 8192 then
            uart_buffer = ""
            emit({type = "error", msg = "Buffer overflow, cleared"})
        end
    end)
end

sys.subscribe("SMS_READY", function()
    sms_ready = true
    read_phone_number()
    sys.publish("SMS_SEND_QUEUE_CHANGED")
end)
sys.subscribe("SMS_INC", function(phone, content)
    log.info("SMS", "发送号码:", phone)
    if config.debug_sms_content then log.info("SMS", "短信内容:", content) end
    if #msg_buffer >= max_msg_buffer then
        log.error("SMS", "receive buffer full; dropping oldest SMS")
        table.remove(msg_buffer, 1)
    end
    msg_buffer[#msg_buffer + 1] = {type = "incoming_sms", timestamp = os.time(), from = phone, content = content}
    sys.publish("NEW_MSG_IN_BUFFER")
end)
sys.subscribe("SIM_IND", function(status)
    if status == "NORDY" or status == "SIM_PIN" then sms_ready = false end
    read_phone_number()
    emit({type = "sim_event", status = status})
end)
sys.subscribe("CC_IND", function(state)
    if state == "INCOMINGCALL" then
        if call_ring_count == 0 then
            emit({type = "incoming_call", from = cc.lastNum() or "unknown"})
        end
        call_ring_count = call_ring_count + 1
    elseif state == "DISCONNECTED" then
        call_ring_count = 0
        emit({type = "call_disconnected"})
    end
end)

sys.taskInit(function()
    while true do
        if #send_queue == 0 then sys.waitUntil("SMS_SEND_QUEUE_CHANGED", 1000) end
        while #send_queue > 0 do
            local item = table.remove(send_queue, 1)
            local ready, waited = sms_ready, 0
            while not ready and waited < 60000 do
                sys.wait(1000)
                waited, ready = waited + 1000, sms_ready
            end
            local valid = not item.expires_at or (transport.now() and transport.now() < item.expires_at)
            local ok, result = false, false
            if ready and valid then
                ok, result = pcall(function() return sms.sendLong(item.to, item.content).wait() end)
            end
            local response = {type = "sms_send_result", request_id = item.request_id, to = item.to,
                success = ok and result == true, timestamp = os.time()}
            if not ready then response.error = "SMS not ready"
            elseif not valid then response.error = "command expired or session disconnected" end
            if item.slot then journal.finish(item.slot, response) end
            emit(response)
        end
    end
end)

sys.taskInit(function()
    while true do
        if #msg_buffer == 0 then sys.waitUntil("NEW_MSG_IN_BUFFER", 1000) end
        if msg_buffer[1] and emit(msg_buffer[1]) then table.remove(msg_buffer, 1) end
        sys.wait(100)
    end
end)

sys.taskInit(function()
    while true do
        local job = table.remove(deferred, 1)
        if job then
            if job.remote then sys.wait(1000) end -- best effort: send accepted response before disconnect
            local ok = pcall(function()
                assert(not job.expires_at or (transport.now() and transport.now() < job.expires_at), "command expired")
                if job.action == "set_flymode" then
                    if flymode_restore_timer then sys.timerStop(flymode_restore_timer) end
                    if job.enabled and job.remote then
                        -- Arm restoration BEFORE dropping cellular connectivity.
                        flymode_restore_timer = sys.timerStart(function()
                            mobile.flymode(0, false)
                            flymode_restore_timer = nil
                        end, job.duration * 1000)
                    end
                    mobile.flymode(0, job.enabled)
                    if job.enabled then sms_ready = false end
                elseif job.action == "reset_stack" then
                    sms_ready = false
                    mobile.reset()
                elseif job.action == "reboot_mcu" then
                    if job.slot then journal.finish(job.slot, {type = "cmd_response", action = job.action,
                        request_id = job.request_id, result = "reboot_requested"}) end
                    pm.reboot()
                end
            end)
            local response = {type = "cmd_response", action = job.action, request_id = job.request_id,
                result = ok and "ok" or "error"}
            if job.slot then journal.finish(job.slot, response) end
            emit(response)
        end
        sys.wait(100)
    end
end)

sys.taskInit(function()
    sys.wait(5000)
    read_phone_number()
    emit({type = "system_ready", project = PROJECT, version = VERSION, locating = locating, features = features()})
    while true do
        sys.wait(config.heartbeat_ms)
        local info = get_mobile_info()
        emit({type = "heartbeat", mobile = info, locating = locating, rssi = info.rssi, signal_level = info.signal_level,
            signal_desc = info.signal_desc, net_reg = info.is_registered, flymode = info.flymode,
            sim_ready = info.sim_ready, sms_ready = info.sms_ready,
            memory_usage = math.floor(collectgarbage("count")), buffer_size = #msg_buffer})
    end
end)

sys.run()
