-- Persistent bounded request journal: prevent replay after MQTT reconnect/reboot.
local M = {}
local config, clock, slots = nil, nil, {}
local count = 64

function M.init(cfg, now)
    config, clock = cfg, now
    assert(fskv and fskv.init and fskv.init(), "MQTT mode requires fskv")
    for i = 1, count do
        local entry = fskv.get("sms_req_" .. i)
        if type(entry) == "table" then slots[i] = entry end
    end
end

function M.begin(cmd)
    local now = clock()
    if not now then return nil, "authenticated server clock unavailable" end
    if type(cmd.request_id) ~= "string" or #cmd.request_id < 1 or #cmd.request_id > 64
        or not cmd.request_id:match("^[%w_-]+$") then return nil, "invalid request_id" end
    if type(cmd.expires_at) ~= "number" or cmd.expires_at % 1 ~= 0 or cmd.expires_at <= now
        or cmd.expires_at > now + config.max_command_ttl then return nil, "invalid/expired command" end
    for i = 1, count do
        local entry = slots[i]
        if entry and entry.id == cmd.request_id then
            return nil, "duplicate request", entry.result or {
                type = "cmd_response", request_id = cmd.request_id, action = entry.action,
                result = "unknown", msg = "accepted earlier; execution may be pending or interrupted; not repeated",
            }
        end
    end
    for i = 1, count do
        if not slots[i] or slots[i].expires_at <= now then
            local entry = {id = cmd.request_id, expires_at = cmd.expires_at, action = cmd.action}
            if not fskv.set("sms_req_" .. i, entry) then return nil, "request journal write failed" end
            slots[i] = entry
            return i
        end
    end
    return nil, "request journal full; wait for commands to expire"
end

function M.finish(index, result)
    if not index then return true end
    local entry = slots[index]
    if not entry or entry.id ~= result.request_id then
        log.warn("CMD", "expired request slot already reused; completion not cached")
        return false
    end
    entry.result = result
    if not fskv.set("sms_req_" .. index, entry) then
        log.error("CMD", "failed to persist result; request remains reserved")
        return false
    end
    return true
end

return M
