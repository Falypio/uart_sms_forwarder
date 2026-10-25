"""Offline tests only; no modem/Broker connection. Requires lupa and cryptography.
Run: python tests/test_air780.py
"""
import json
import os
import sys
from pathlib import Path
import unittest

from lupa.lua53 import LuaRuntime, lua_type
from cryptography.hazmat.primitives import hashes, hmac, serialization
from cryptography.hazmat.primitives.asymmetric import rsa, padding
from cryptography.hazmat.primitives.ciphers.aead import AESGCM

ROOT = Path(__file__).resolve().parent
if not (ROOT / 'main.lua').exists():
    ROOT = ROOT.parent


class Harness:
    def __init__(self):
        self.lua = LuaRuntime(encoding=None, unpack_returned_tuples=True)
        self.g = self.lua.globals()
        self.private_key = rsa.generate_private_key(public_exponent=65537, key_size=2048)
        self.public_pem = self.private_key.public_key().public_bytes(
            serialization.Encoding.PEM, serialization.PublicFormat.SubjectPublicKeyInfo)
        self.g.py_encode = lambda t: json.dumps(self.python(t), ensure_ascii=False, separators=(',', ':')).encode()
        self.g.py_decode = lambda s: self.table(json.loads(s))
        self.g.py_random = os.urandom
        self.g.py_hmac = self.hmac
        self.g.py_encrypt = self.encrypt
        self.g.py_decrypt = self.decrypt
        self.g.py_rsa = lambda key, data: serialization.load_pem_public_key(key).encrypt(data, padding.PKCS1v15())
        self.g.py_public = self.public_pem
        self.g.TEST_PATH = str(ROOT).replace('\\', '/').encode()
        self.lua.execute(b'''
            package.path = TEST_PATH .. "/?.lua;" .. package.path
            json = {encode=py_encode, decode=py_decode}
            crypto = {trng=py_random, hmac_sha256=py_hmac, cipher_encrypt=py_encrypt, cipher_decrypt=py_decrypt}
            rsa = {encrypt=py_rsa}
            logs, writes, timers, tasks, subs = {}, {}, {}, {}, {}
            now_ms, serial_setups, mqtt_creates, sms_sends = 0, 0, 0, 0
            log = {}
            for _,level in ipairs({"info","warn","error"}) do
                log[level] = function(...) logs[#logs+1] = {...} end
            end
            sys = {}
            function sys.subscribe(event, fn) subs[event] = fn end
            function sys.publish(event, ...) if subs[event] then subs[event](...) end end
            function sys.taskInit(fn) tasks[#tasks+1] = {co=coroutine.create(fn), due=now_ms} end
            function sys.wait(ms) coroutine.yield(ms or 100) end
            function sys.waitUntil(event, ms) coroutine.yield(ms or 100); return false end
            function sys.timerLoopStart(fn, ms)
                timers[#timers+1]={fn=fn,due=now_ms+ms,period=ms}; return #timers
            end
            function sys.timerStart(fn, ms) timers[#timers+1]={fn=fn,due=now_ms+ms}; return #timers end
            function sys.timerStop(id) if timers[id] then timers[id].cancelled=true end end
            function sys.run() end
            package.loaded.sys = sys
            package.loaded.sysplus = true
            function advance(ms)
                local end_at = now_ms + ms
                while now_ms <= end_at do
                    if py_poll and fake_client then
                        local payload=py_poll()
                        if payload then deliver(payload) end
                    end
                    for _, t in ipairs(tasks) do
                        if coroutine.status(t.co) ~= "dead" and t.due <= now_ms then
                            local ok, delay = coroutine.resume(t.co)
                            assert(ok, delay)
                            t.due = now_ms + (delay or 100)
                        end
                    end
                    for _,t in ipairs(timers) do
                        if not t.cancelled and t.due <= now_ms then
                            if t.period then t.due=t.due+t.period else t.cancelled=true end
                            t.fn()
                        end
                    end
                    now_ms=now_ms+100
                end
            end
            mobile = {
                imei=function() return "123456789012345" end, status=function() return 1 end,
                iccid=function() return "test-iccid" end, imsi=function() return "test-imsi" end,
                number=function() return "13800000000" end, csq=function() return 20 end,
                rssi=function() return -60 end, rsrp=function() return -85 end, rsrq=function() return -10 end,
                flymode=function(index,value) if value ~= nil then last_flymode=value end; return last_flymode or false end,
                reset=function() reset_called=true end,
            }
            mcu = {ticks2=function(mode) assert(mode==2); return 0, math.floor(now_ms/1000) end}
            rtos={bsp=function() return "Air780EPM" end, version=function() return "test" end}
            led_writes, led_level = {}, nil
            gpio={setup=function(pin, initial)
                assert(pin==21 and initial==0)
                led_level=initial
                return function(level) led_level=level; led_writes[#led_writes+1]=level end
            end}
            pm={reboot=function() reboot_called=true end}
            cc={lastNum=function() return "10086" end}
            uart={VUART_0=0, setup=function() serial_setups=serial_setups+1 end,
                on=function(id,event,fn) uart_receive=fn end,
                read=function() local s=uart_input; uart_input=nil; return s end,
                write=function(id,s) writes[#writes+1]=s; return #s end}
            sms={sendLong=function(to,content) sms_sends=sms_sends+1; return {wait=function() return true end} end}
            kv={}
            fskv={init=function() return true end, get=function(k) return kv[k] end,
                set=function(k,v) kv[k]=json.decode(json.encode(v)); return true end}
            socket={LWIP_GP=1,adapter=function() return true end,sntp=function() end}
            mqtt={create=function() mqtt_creates=mqtt_creates+1; error("unexpected MQTT creation") end}
        ''')

    def python(self, v):
        if lua_type(v) == 'table':
            return {self.python(k): self.python(val) for k, val in v.items()}
        return v.decode() if isinstance(v, bytes) else v

    def table(self, v):
        if isinstance(v, dict):
            return self.lua.table_from({k.encode(): self.table(val) for k, val in v.items()})
        if isinstance(v, list):
            return self.lua.table_from([self.table(x) for x in v])
        return v.encode() if isinstance(v, str) else v

    @staticmethod
    def hmac(data, key):
        h = hmac.HMAC(key, hashes.SHA256()); h.update(data)
        return h.finalize().hex().encode()

    @staticmethod
    def encrypt(alg, pad, data, key, iv):
        assert alg == b'AES-256-GCM' and pad == b'NONE'
        encrypted = AESGCM(key).encrypt(iv, data, None)
        return encrypted[:-16], encrypted[-16:]

    @staticmethod
    def decrypt(alg, pad, data, key, iv, tag):
        try:
            return AESGCM(key).decrypt(iv, data + tag, None)
        except Exception:
            return None

    def run_main(self, mode='serial'):
        self.g.TEST_MODE = mode.encode()
        self.lua.execute(b'''
            local cfg=require "device_config"
            cfg.access_mode=TEST_MODE
            cfg.mqtt.public_key=py_public
            cfg.mqtt.host="broker.example.invalid"
            cfg.mqtt.username="test-device"
            cfg.mqtt.password="test-password"
            cfg.mqtt.registration_token=string.rep("t",32)
        ''')
        self.lua.execute((ROOT / 'main.lua').read_bytes())


class Air780Tests(unittest.TestCase):
    def test_all_files_parse(self):
        h = Harness()
        for p in ROOT.glob('*.lua'):
            h.lua.execute(b'assert(load(...))', p.read_bytes())

    def test_serial_compatibility_and_sms(self):
        h = Harness(); h.run_main()
        h.lua.execute(b'''
            assert(serial_setups==1 and mqtt_creates==0)
            uart_input='CMD_START:{"action":"get_sta'
            uart_receive(0,#uart_input)
            assert(#writes==0)
            uart_input='tus"}:CMD_END\\r\\n'
            uart_receive(0,#uart_input)
            assert(#writes==1 and writes[1]:find('status_response',1,true))
            subs.SMS_READY()
            subs.SMS_INC("10086", "test SMS")
            advance(500)
            assert(writes[#writes]:find("incoming_sms",1,true))
            process_uart_command({action="send_sms",to="10086",content="hello",request_id="serial1"})
            advance(1500)
            assert(sms_sends==1)
            process_uart_command({action="set_flymode",enabled="false"})
            advance(500)
            assert(last_flymode==false)
            process_uart_command({action="set_flymode",enabled="bad"})
            assert(writes[#writes]:find("invalid enabled",1,true))
        ''')

    def test_locator_blinks_and_stops_without_blocking_serial(self):
        h = Harness()
        h.run_main('serial')
        h.lua.execute(b'''
            assert(led_level==0)
            process_uart_command({action="set_locate",enabled=true,request_id="find1"})
            assert(led_level==1)
            local timer_count=#timers
            process_uart_command({action="set_locate",enabled=true,request_id="find2"})
            assert(#timers==timer_count)
            advance(400); assert(led_level==1)
            advance(100); assert(led_level==0)
            advance(500); assert(led_level==1)
            process_uart_command({action="get_status",request_id="status1"})
            assert(writes[#writes]:find('"locating":true',1,true))
            assert(writes[#writes]:find('"locate_led":true',1,true))
            process_uart_command({action="set_locate",enabled=false,request_id="stop1"})
            assert(led_level==0)
            local count=#led_writes
            advance(2000); assert(led_level==0 and #led_writes==count)
            process_uart_command({action="set_locate",enabled="bad",request_id="bad1"})
            assert(led_level==0 and writes[#writes]:find("invalid enabled",1,true))
            process_uart_command({action="set_locate",enabled=true,device_id="another-device"})
            assert(led_level==0)
        ''')

    def test_heartbeat_refreshes_uptime(self):
        h = Harness()
        h.run_main('serial')
        h.lua.execute(b'''
            advance(125000)
            local initial, previous, count
            count=0
            for _, wire in ipairs(writes) do
                local event=json.decode(wire:match("^SMS_START:(.-):SMS_END"))
                if event.type=="system_ready" then initial=event.features.mobile.uptime end
                if event.type=="heartbeat" then
                    assert(event.mobile and event.mobile.uptime, "heartbeat must include current uptime")
                    assert(event.mobile.uptime >= 60 and event.mobile.uptime > initial)
                    if previous then assert(event.mobile.uptime-previous==60) end
                    previous=event.mobile.uptime
                    count=count+1
                end
            end
            assert(count==2 and previous>=120)
            process_uart_command({action="get_status"})
            local status=json.decode(writes[#writes]:match("^SMS_START:(.-):SMS_END"))
            assert(status.mobile.uptime>=previous)
        ''')

    def test_locator_unavailable_does_not_break_device(self):
        h = Harness()
        h.lua.execute(b'gpio=nil')
        h.run_main('serial')
        h.lua.execute(b'''
            process_uart_command({action="set_locate",enabled=true})
            assert(writes[#writes]:find("locator unavailable",1,true))
            process_uart_command({action="get_status"})
            assert(writes[#writes]:find('"locate_led":false',1,true))
        ''')

    def test_crypto_wire_and_tampering(self):
        self.check_crypto_wire_and_tampering(ignore_tag=False)

    def test_crypto_firmware_ignores_tag(self):
        self.check_crypto_wire_and_tampering(ignore_tag=True)

    def check_crypto_wire_and_tampering(self, ignore_tag):
        h = Harness()
        if ignore_tag:
            from cryptography.hazmat.primitives.ciphers import Cipher, algorithms, modes

            def unchecked_decrypt(alg, pad, data, key, iv, tag):
                # Reproduce the modem: return plaintext without finalizing/tag verification.
                return Cipher(algorithms.AES(key), modes.GCM(iv)).decryptor().update(data)

            h.g.py_unchecked_decrypt = unchecked_decrypt
            h.lua.execute(b'crypto.cipher_decrypt=py_unchecked_decrypt')
        h.lua.execute(b'''
            secure=require "device_crypto"
            session=secure.new({public_key=py_public,key_id="test-key"},"device1")
            registration=secure.seal(session,"register",{registration_token="secret"})
        ''')
        packet = json.loads(h.g.registration)
        root = h.private_key.decrypt(bytes.fromhex(packet['wrapped_key']), padding.PKCS1v15())
        self.assertEqual(len(root), 32)
        keys = {direction: bytes.fromhex(h.hmac(f"sms-air780-v1|{packet['sid']}|{direction}".encode(), root).decode())
                for direction in ['up', 'down']}
        plain = AESGCM(keys['up']).decrypt(bytes.fromhex(packet['iv']),
            bytes.fromhex(packet['ciphertext'] + packet['tag']), None)
        self.assertEqual(json.loads(plain)['body']['registration_token'], 'secret')

        def down(seq=1, body=None):
            inner = dict(v=1, device_id='device1', sid=packet['sid'], direction='down', seq=seq,
                         kind='command', body=body or {'action': 'get_status'})
            nonce = bytes.fromhex('0000000000000002' + f'{seq:016x}')
            encrypted = AESGCM(keys['down']).encrypt(nonce, json.dumps(inner).encode(), None)
            return dict(v=1, device_id='device1', sid=packet['sid'], direction='down', seq=seq,
                        kind='command', iv=nonce.hex(), ciphertext=encrypted[:-16].hex(), tag=encrypted[-16:].hex())

        wire = down(); h.g.packet = json.dumps(wire).encode()
        h.lua.execute(b'local kind,body=secure.open(session,packet); assert(kind=="command" and body.action=="get_status")')
        h.lua.execute(b'assert(not pcall(secure.open,session,packet))')
        wire = down(2); wire['tag'] = '00' * 16
        h.g.packet = json.dumps(wire).encode()
        h.lua.execute(b'assert(not pcall(secure.open,session,packet)); assert(session.rx==1)')
        # A forged tag must fail before JSON plaintext parsing, with rx unchanged.
        h.lua.execute(b'''
            local decode=json.decode
            json.decode=function(s) assert(s==packet, "unverified plaintext parsed"); return decode(s) end
            local ok,err=pcall(secure.open,session,packet)
            assert(not ok and tostring(err):find("authentication failed",1,true)); assert(session.rx==1)
            json.decode=decode
        ''')
        wire = down(2)
        corrupted = bytearray.fromhex(wire['ciphertext'])
        corrupted[-2] ^= 1
        wire['ciphertext'] = corrupted.hex()
        h.g.packet = json.dumps(wire).encode()
        h.lua.execute(b'assert(not pcall(secure.open,session,packet)); assert(session.rx==1)')
        # Rejecting forged data must not prevent a valid packet at the same sequence.
        h.g.packet = json.dumps(down(2)).encode()
        h.lua.execute(b'assert(secure.open(session,packet)=="command"); assert(session.rx==2)')
        wire = down(3); wire['kind'] = 'register_result'
        h.g.packet = json.dumps(wire).encode()
        h.lua.execute(b'assert(not pcall(secure.open,session,packet)); assert(session.rx==2)')

    def test_journal_replay_reboot_and_expiry(self):
        h = Harness()
        h.lua.execute(b'''
            journal=require "device_commands"
            local cfg={max_command_ttl=300}
            journal.init(cfg,function() return 1800000000 end)
            local cmd={request_id="test1",action="send_sms",expires_at=1800000060}
            local slot=assert(journal.begin(cmd))
            assert(not journal.begin(cmd))
            journal.init(cfg,function() return 1800000000 end)
            assert(not journal.begin(cmd))
            journal.finish(slot,{type="sms_send_result",request_id="test1",success=true})
            local s,e,previous=journal.begin(cmd)
            assert(not s and previous.success)
            assert(not journal.begin({request_id="bad",expires_at=1799999999}))
            assert(not journal.begin({request_id="bad",expires_at=1800000301}))
            for i=2,64 do assert(journal.begin({request_id="id"..i,expires_at=1800000060})) end
            assert(not journal.begin({request_id="overflow",expires_at=1800000060}))
        ''')

    def test_mqtt_dns_diagnostic_and_cleanup(self):
        for mode in ['success', 'timeout', 'error']:
            with self.subTest(mode=mode):
                h = Harness()
                h.g.DNS_MODE = mode.encode()
                h.lua.execute(b'''
                    dns_closed, dns_released, dns_calls = 0, 0, 0
                    socket.ON_LINE, socket.CLOSED = 2, 5
                    socket.create=function(adapter, cb)
                        assert(adapter==socket.LWIP_GP)
                        return {cb=cb}
                    end
                    socket.config=function(ctrl) return true end
                    socket.connect=function(ctrl, host, port, ipv6)
                        assert(host=="broker.example.invalid" and port==0 and ipv6==false)
                        dns_calls=dns_calls+1
                        if DNS_MODE=="success" then ctrl.cb(ctrl,socket.ON_LINE,0)
                        elseif DNS_MODE=="error" then ctrl.cb(ctrl,socket.ON_LINE,-1) end
                        return true,false
                    end
                    socket.remoteIP=function(ctrl) return "192.0.2.1", "192.0.2.2" end
                    socket.close=function(ctrl) dns_closed=dns_closed+1 end
                    socket.release=function(ctrl) dns_released=dns_released+1 end
                ''')
                h.run_main('mqtt')
                h.lua.execute(b'''
                    advance(DNS_MODE=="timeout" and 5500 or 500)
                    assert(dns_calls==1 and dns_closed==1 and dns_released==1)
                    assert(mqtt_creates==1) -- Even a diagnostic timeout cannot block MQTT.
                    local found=false
                    for _,line in ipairs(logs) do
                        if line[1]=="DNS" then
                            if DNS_MODE=="success" then
                                assert(line[4]=="192.0.2.1, 192.0.2.2")
                            else
                                assert(line[4]=="continuing MQTT connection")
                            end
                            found=true
                        end
                    end
                    assert(found)
                ''')

    def test_mqtt_mode_does_not_open_serial_and_requires_auth(self):
        h = Harness(); h.run_main('mqtt')
        h.lua.execute(b'''
            assert(serial_setups==0 and uart_receive==nil)
            assert((require "device_mqtt").now()==nil)
            advance(300)
            assert(mqtt_creates==1)
            assert(sms_sends==0)
        ''')

    def test_mqtt_register_command_ack_reconnect_and_flymode(self):
        h = Harness()
        server = {'seq': 0, 'events': [], 'registrations': 0}
        deliveries = []
        h.g.py_poll = lambda: deliveries.pop(0) if deliveries else None

        def send_down(kind, body):
            server['seq'] += 1
            seq = server['seq']
            inner = dict(v=1, device_id='123456789012345', sid=server['sid'], direction='down',
                         seq=seq, kind=kind, body=body)
            nonce = bytes.fromhex('0000000000000002' + f'{seq:016x}')
            encrypted = AESGCM(server['down']).encrypt(nonce, json.dumps(inner).encode(), None)
            wire = dict(inner)
            del wire['body']
            wire.update(iv=nonce.hex(), ciphertext=encrypted[:-16].hex(), tag=encrypted[-16:].hex())
            deliveries.append(json.dumps(wire).encode())

        def publish(topic, payload):
            packet = json.loads(payload)
            if packet['kind'] == 'register':
                server['registrations'] += 1
                root = h.private_key.decrypt(bytes.fromhex(packet['wrapped_key']), padding.PKCS1v15())
                server['sid'], server['seq'] = packet['sid'], 0
                for direction in ['up', 'down']:
                    server[direction] = bytes.fromhex(h.hmac(
                        f"sms-air780-v1|{packet['sid']}|{direction}".encode(), root).decode())
            clear = AESGCM(server['up']).decrypt(bytes.fromhex(packet['iv']),
                bytes.fromhex(packet['ciphertext'] + packet['tag']), None)
            body = json.loads(clear)['body']
            if packet['kind'] == 'register':
                self.assertEqual(body['registration_token'], 't' * 32)
                send_down('register_result', {'accepted': True, 'server_time': 1800000000})
            else:
                server['events'].append(body['data'])
                send_down('ack', {'message_id': body['message_id']})
            return 1

        h.g.py_publish = publish
        h.lua.execute(b'''
            mqtt.create=function()
                mqtt_creates=mqtt_creates+1
                local c={}
                function c:on(fn) self.cb=fn; fake_client=self end
                function c:auth(...) return true end
                function c:keepalive(...) end
                function c:connect() self.cb(self,"conack"); return true end
                function c:subscribe(topic,qos) self.cb(self,"suback",true,1); return true end
                function c:publish(topic,payload,qos,retain) assert(type(retain)=="number" and qos==1 and retain==0); return py_publish(topic,payload) end
                function c:close() end
                return c
            end
            function deliver(payload)
                fake_client.cb(fake_client,"recv","sms/v1/devices/123456789012345/down",payload)
            end
        ''')
        h.run_main('mqtt')
        h.lua.execute(b'advance(1000); assert((require "device_mqtt").now()~=nil); subs.SMS_READY()')
        cmd = dict(device_id='123456789012345', request_id='mqtt-send1', action='send_sms',
                   to='10086', content='hello', expires_at=1800000100)
        send_down('command', cmd)
        h.lua.execute(b'advance(1500); assert(sms_sends==1)')
        send_down('command', cmd)
        h.lua.execute(b'advance(500); assert(sms_sends==1)')
        self.assertTrue(any(x.get('type') == 'sms_send_result' and x.get('success') for x in server['events']))
        old_sid = server['sid']
        h.lua.execute(b'fake_client.cb(fake_client,"disconnect"); advance(6500)')
        self.assertNotEqual(old_sid, server['sid'])
        self.assertEqual(server['registrations'], 2)
        send_down('command', cmd)
        h.lua.execute(b'advance(500); assert(sms_sends==1)')
        # The encrypted MQTT control path uses the same GPIO21 handler.
        locate = dict(device_id='123456789012345', request_id='locate1', action='set_locate',
                      enabled=True, expires_at=1800000100)
        send_down('command', locate)
        h.lua.execute(b'advance(500); assert(led_level==0)')
        self.assertTrue(any(x.get('action') == 'set_locate' and x.get('locating') is True
                            and x.get('result') == 'ok' for x in server['events']))
        send_down('command', locate) # Replayed request must not restart the LED timer.
        h.lua.execute(b'advance(500); assert(led_level==1)')
        locate.update(request_id='locate-stop1', enabled=False)
        send_down('command', locate)
        h.lua.execute(b'advance(1000); assert(led_level==0)')
        self.assertTrue(any(x.get('action') == 'set_locate' and x.get('locating') is False
                            and x.get('result') == 'ok' for x in server['events']))
        # Remote flight mode must have an automatic restoration deadline.
        cmd2 = dict(device_id='123456789012345', request_id='flight1', action='set_flymode',
                    enabled=True, expires_at=1800000100)
        send_down('command', cmd2)
        h.lua.execute(b'advance(500); assert(last_flymode~=true)')
        cmd2['duration_seconds'] = 2
        send_down('command', cmd2)
        h.lua.execute(b'advance(1500); assert(last_flymode==true); advance(2500); assert(last_flymode==false)')
        h.lua.execute(b'advance(65000)')
        heartbeats = [x for x in server['events'] if x.get('type') == 'heartbeat']
        self.assertTrue(heartbeats)
        self.assertGreaterEqual(heartbeats[-1]['mobile']['uptime'], 60)


if __name__ == '__main__':
    unittest.main(verbosity=2)
