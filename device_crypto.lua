-- Protocol sms-air780-v1. Binary fields are lowercase hex; see AIR780.md.
local M = {}

function M.hex(s)
    return (s:gsub(".", function(c) return string.format("%02x", c:byte()) end))
end

function M.unhex(s)
    assert(type(s) == "string" and #s % 2 == 0 and not s:find("[^0-9a-fA-F]"), "invalid hex")
    return (s:gsub("..", function(c) return string.char(tonumber(c, 16)) end))
end

local function random(n)
    local value = crypto.trng(n)
    assert(type(value) == "string" and #value == n, "TRNG unavailable")
    return value
end

local function derive(root, sid, label)
    return M.unhex(crypto.hmac_sha256("sms-air780-v1|" .. sid .. "|" .. label, root))
end

local function iv(direction, seq)
    -- 16-byte IV for LuatOS compatibility; unique within each directional key.
    return M.unhex((direction == "up" and "0000000000000001" or "0000000000000002")
        .. string.format("%016x", seq))
end

-- Firmware may decrypt GCM without checking the supplied tag. Recompute it
-- locally and authenticate before returning plaintext to the JSON/command layer.
local function same_tag(a, b)
    if type(a) ~= "string" or type(b) ~= "string" or #a ~= 16 or #b ~= 16 then return false end
    local difference = 0
    for i = 1, 16 do difference = difference + math.abs(a:byte(i) - b:byte(i)) end
    return difference == 0
end

local function authenticated_decrypt(ciphertext, key, nonce, tag)
    if type(tag) ~= "string" or #tag ~= 16 then return nil end
    local plain = crypto.cipher_decrypt("AES-256-GCM", "NONE", ciphertext, key, nonce, tag)
    if type(plain) ~= "string" then return nil end
    -- Verification only: this recomputed ciphertext is never published.
    local checked_ciphertext, checked_tag = crypto.cipher_encrypt("AES-256-GCM", "NONE", plain, key, nonce)
    if checked_ciphertext ~= ciphertext or not same_tag(checked_tag, tag) then return nil end
    return plain
end

function M.new(config, device_id)
    assert(crypto and crypto.trng and crypto.hmac_sha256 and crypto.cipher_encrypt
        and crypto.cipher_decrypt and rsa and rsa.encrypt, "firmware lacks crypto/RSA")
    local key = config.public_key
    if not key or key == "" then key = io.readFile(config.public_key_file) end
    assert(type(key) == "string" and key:find("PUBLIC KEY", 1, true),
        "RSA PUBLIC KEY PEM required; export public key from certificate first")
    local root, sid = random(32), M.hex(random(16))
    local wrapped = rsa.encrypt(key, root)
    assert(type(wrapped) == "string" and #wrapped >= 256, "RSA key must be at least 2048 bits")
    local session = {
        device_id = device_id, sid = sid, key_id = config.key_id,
        wrapped_key = M.hex(wrapped), tx = 0, rx = 0,
        up_key = derive(root, sid, "up"), down_key = derive(root, sid, "down"),
    }
    -- Test the entire authentication path, including firmware that ignores tags.
    local nonce = random(16)
    local ciphertext, tag = crypto.cipher_encrypt("AES-256-GCM", "NONE", "self-test", session.up_key, nonce)
    assert(type(ciphertext) == "string" and type(tag) == "string" and #tag == 16, "AES-GCM unavailable")
    assert(authenticated_decrypt(ciphertext, session.up_key, nonce, tag) == "self-test",
        "AES-GCM authenticated roundtrip failed")
    local badtag = string.char((tag:byte(1) + 1) % 256) .. tag:sub(2)
    local ok, result = pcall(authenticated_decrypt, ciphertext, session.up_key, nonce, badtag)
    assert(not ok or result == nil, "AES-GCM tag verification self-test failed")
    local badciphertext = string.char((ciphertext:byte(1) + 1) % 256) .. ciphertext:sub(2)
    ok, result = pcall(authenticated_decrypt, badciphertext, session.up_key, nonce, tag)
    assert(not ok or result == nil, "AES-GCM ciphertext verification self-test failed")
    if log and log.info then log.info("CRYPTO", "AES-GCM verified decrypt self-test passed") end
    return session
end

function M.seal(session, kind, body)
    assert(session.tx < 2147483647, "session sequence exhausted")
    session.tx = session.tx + 1 -- never roll back, including on publish failure
    local seq = session.tx
    -- Bind routing metadata inside authenticated plaintext; API has no AAD argument.
    local plain = assert(json.encode({v = 1, device_id = session.device_id, sid = session.sid,
        direction = "up", seq = seq, kind = kind, body = body}))
    local nonce = iv("up", seq)
    local data, tag = crypto.cipher_encrypt("AES-256-GCM", "NONE", plain, session.up_key, nonce)
    assert(type(data) == "string" and type(tag) == "string" and #tag == 16, "encryption failed")
    local packet = {v = 1, device_id = session.device_id, sid = session.sid, direction = "up",
        seq = seq, kind = kind, iv = M.hex(nonce), ciphertext = M.hex(data), tag = M.hex(tag)}
    if kind == "register" then
        packet.key_id, packet.wrapped_key = session.key_id, session.wrapped_key
    end
    return assert(json.encode(packet))
end

function M.open(session, payload)
    local p = json.decode(payload)
    assert(type(p) == "table" and p.v == 1 and p.device_id == session.device_id
        and p.sid == session.sid and p.direction == "down", "wrong session or device")
    assert(type(p.seq) == "number" and p.seq % 1 == 0 and p.seq > session.rx
        and p.seq <= 2147483647, "replayed or invalid sequence")
    assert(p.iv == M.hex(iv("down", p.seq)), "invalid IV")
    assert(type(p.tag) == "string" and #p.tag == 32, "invalid tag")
    local plain = authenticated_decrypt(M.unhex(p.ciphertext),
        session.down_key, M.unhex(p.iv), M.unhex(p.tag))
    assert(type(plain) == "string", "authentication failed")
    local inner = json.decode(plain)
    assert(type(inner) == "table" and inner.v == p.v and inner.device_id == p.device_id
        and inner.sid == p.sid and inner.seq == p.seq and inner.kind == p.kind
        and inner.direction == p.direction and type(inner.body) == "table", "metadata mismatch")
    session.rx = p.seq
    return p.kind, inner.body
end

return M
