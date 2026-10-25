// Headless browser checks using mock APIs only. No real modem, account or Broker.
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');
const net = require('node:net');
const {spawn} = require('node:child_process');
const {chromium} = require('playwright');

const delay = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const publicKey = '-----BEGIN PUBLIC KEY-----\nOFFLINE-TEST-PUBLIC-KEY\n-----END PUBLIC KEY-----\n';

async function main() {
    const port = await new Promise((resolve, reject) => {
        const server = net.createServer();
        server.on('error', reject);
        server.listen(0, '127.0.0.1', () => {const port = server.address().port; server.close(() => resolve(port));});
    });
    const cwd = path.resolve(__dirname, '..');
    const output = path.join(cwd, 'test-results');
    await fs.mkdir(output, {recursive: true});
    let serverLog = '';
    const server = spawn(process.execPath, [path.join(cwd, 'node_modules/vite/bin/vite.js'), '--host', '127.0.0.1', '--port', String(port), '--strictPort'], {cwd, windowsHide: true, stdio: ['ignore', 'pipe', 'pipe']});
    server.stdout.on('data', (chunk) => {serverLog += chunk;});
    server.stderr.on('data', (chunk) => {serverLog += chunk;});
    let browser;
    try {
        const base = `http://127.0.0.1:${port}`;
        let ready = false;
        for (let i = 0; i < 150; i++) {try {if ((await fetch(base)).ok) {ready = true; break;}} catch {} await delay(200);}
        assert(ready, `Vite failed to start: ${serverLog}`);
        browser = await chromium.launch({headless: true, ...(process.env.PLAYWRIGHT_CHANNEL ? {channel: process.env.PLAYWRIGHT_CHANNEL} : {})});
        const context = await browser.newContext({viewport: {width: 1440, height: 1000}, acceptDownloads: true});
        const requests = [];
        const errors = [];
        const unexpected = [];
        const mobile = (number) => ({sim_ready: true, sms_ready: true, number, iccid: `ICCID-${number}`, imsi: '460001234567890', rssi: -65, rsrp: -88, rsrq: -9, csq: 22, signal_level: 22, signal_desc: '强', is_registered: true, uptime: 3600});
        const records = [
            {id: 'deviceA', name: 'USB A', accessMode: 'serial', online: true, port: '/dev/ttyUSB0'},
            {id: 'deviceB', name: 'MQTT B', accessMode: 'mqtt', online: true, port: ''},
            {id: 'deviceC', name: 'Offline C', accessMode: 'serial', online: false, port: '/dev/ttyUSB1'},
        ].map((d, i) => ({...d, lastSeen: Date.now(), createdAt: Date.now(), features: {locate_led: true, locate_gpio: 21, imei: `12345678901234${i}`, model: 'Air780EPM', firmware: 'V2050', mobile: mobile(`1380000000${i + 1}`)}, status: {locating: false, mobile: mobile(`1380000000${i + 1}`), memory_usage: 96}}));
        records[0].status.mobile.imsi = '460011234567890';
        records[1].status.mobile.operator = '测试运营商';
        const sms = Object.fromEntries(records.map((d) => [d.id, [{id: `msg-${d.id}`, deviceId: d.id, from: '10086', to: '', content: `只属于 ${d.id} 的短信`, type: 'incoming', status: 'received', createdAt: Date.now(), updatedAt: Date.now()}]]));
        const channels = Object.fromEntries(records.map((d) => [d.id, [{type: 'dingtalk', enabled: true, config: {secretKey: `SAVED_${d.id}`, signSecret: ''}}]]));
        const tasks = Object.fromEntries(records.map((d) => [d.id, [{id: `task-${d.id}`, deviceId: d.id, name: `任务-${d.id}`, enabled: false, intervalDays: 90, phoneNumber: '10086', content: d.id}]]));
        const locateCommands = new Map();
        let failLocate = false;
        let delaySMS = false;
        let delayAConversations = false;
        let failList = false;
        let oidcExchanges = 0;
        const handler = async (route) => {
            const request = route.request();
            const url = new URL(request.url());
            const p = url.pathname.slice(4);
            const method = request.method();
            const body = request.postData() ? JSON.parse(request.postData()) : null;
            requests.push({path: p, method, body});
            const json = (data, status = 200) => route.fulfill({status, contentType: 'application/json', body: JSON.stringify(data)});
            const empty = () => route.fulfill({status: 204});
            if (p === '/auth/config') return json({passwordEnabled: true, oidcEnabled: false});
            if (p === '/login') return json({token: 'offline-test-token', username: 'operator', expiresAt: Date.now() + 3600000});
            if (p === '/auth/oidc/callback') {oidcExchanges++; await delay(120); return json({token: 'offline-test-token', username: 'operator'});}
            if (request.headers().authorization !== 'Bearer offline-test-token') return json({error: 'unauthorized'}, 401);
            if (p === '/version') return json({version: 'offline-test'});
            if (p === '/devices' && method === 'GET') return failList ? json({error: '列表暂时不可用'}, 503) : json(records);
            if (p === '/devices/server-public-key') return json({keyId: 'server-key-1', publicKey});
            if (p === '/devices/enroll') return json({deviceId: body.deviceId, registrationToken: 'OFFLINE_TEST_TOKEN_NOT_REAL_1234567890', keyId: 'server-key-1', publicKey, topicPrefix: 'sms/v1/devices'}, 201);
            if (p.startsWith('/device-enrollments/')) return empty();
            const match = p.match(/^\/devices\/(device[A-C])(?:\/(.*))?$/);
            if (!match) {unexpected.push(p); return json({error: 'unexpected mock route'}, 404);}
            const [, id, tail = ''] = match;
            const device = records.find((d) => d.id === id);
            if (!tail && method === 'GET') return json(device);
            if (!tail && method === 'PUT') {device.name = body.name; return empty();}
            if (tail === 'status') return json({...device.status, connected: device.online, port_name: device.port, accessMode: device.accessMode, deviceId: id});
            if (tail === 'messages/stats') return json({totalCount: sms[id].length, incomingCount: sms[id].length, outgoingCount: 0, todayCount: sms[id].length});
            if (tail === 'messages/conversations') {
                if (id === 'deviceA' && delayAConversations) await delay(900);
                return json(sms[id].length ? [{peer: '10086', lastMessage: sms[id][0], messageCount: sms[id].length}] : []);
            }
            if (tail.startsWith('messages/conversations/') && tail.endsWith('/messages')) return json(sms[id]);
            if (tail.startsWith('messages') && method === 'DELETE') {sms[id] = []; return empty();}
            if (tail === 'properties/notification_channels') {
                if (method === 'PUT') {channels[id] = body.value; return empty();}
                return json({id: 'notification_channels', value: channels[id]});
            }
            if (tail === 'properties/auto_flymode_config') return method === 'PUT' ? empty() : json({id: 'auto_flymode_config', value: {enabled: false, idleTimeoutHours: 1}});
            if (tail.startsWith('notifications/')) return empty();
            if (tail === 'scheduled-tasks' && method === 'GET') return json(tasks[id]);
            if (tail === 'scheduled-tasks' && method === 'POST') {const task = {...body, id: `created-${id}`, deviceId: id}; tasks[id].push(task); return json(task, 201);}
            if (tail.startsWith('scheduled-tasks/') && method === 'DELETE') {tasks[id] = tasks[id].filter((t) => t.id !== tail.split('/')[1]); return empty();}
            if (tail.startsWith('scheduled-tasks/') && method === 'PUT') return json({...body, id: tail.split('/')[1], deviceId: id});
            if (tail === 'locate' && method === 'POST') {
                const cmd = {id: `locate-${id}-${locateCommands.size}`, deviceId: id, action: 'set_locate', status: 'pending', result: '', createdAt: Date.now(), expiresAt: Date.now()/1000+120};
                locateCommands.set(cmd.id, {cmd, enabled: body.enabled, fail: failLocate, polls: 0});
                return json(cmd, 202);
            }
            if (tail.startsWith('commands/')) {
                const item = locateCommands.get(tail.split('/')[1]);
                if (!item || item.cmd.deviceId !== id) return json({error:'not found'},404);
                if (++item.polls > 1) {
                    item.cmd.status = item.fail ? 'failed' : 'succeeded';
                    if (!item.fail) device.status.locating = item.enabled;
                    item.cmd.result = JSON.stringify({locating: device.status.locating});
                }
                return json(item.cmd);
            }
            if (tail === 'commands') return json([]);
            if (tail.startsWith('serial/') || tail.endsWith('/trigger')) {
                if (tail === 'serial/sms' && delaySMS) await delay(850);
                return json({id: `command-${id}`, deviceId: id, action: tail.split('/')[1], status: 'pending', createdAt: Date.now()}, 202);
            }
            unexpected.push(p); return json({error: 'unexpected mock route'}, 404);
        };
        await context.route('**/api/**', async (route) => {
            if (!new URL(route.request().url()).pathname.startsWith('/api/')) return route.continue();
            try {await handler(route);} catch (error) {
                if (!/closed|handled|cancel|abort/i.test(String(error))) throw error;
            }
        });
        const page = await context.newPage();
        page.on('pageerror', (error) => {errors.push(error.message); console.error('Browser runtime:', error.message);});
        const text = async (value) => page.getByText(value, {exact: false}).first().waitFor({state: 'visible'});
        const bodyHas = async (value) => assert((await page.locator('body').innerText()).includes(value), `Missing ${value}`);
        const switchTo = async (id) => {
            const expected=new URL(page.url());
            expected.pathname=expected.pathname.replace(/^\/devices\/[^/]+/,`/devices/${id}`);
            await page.getByLabel('切换设备').selectOption(id);
            await page.waitForURL(expected.href);
            await page.waitForFunction((value)=>document.querySelector('select[aria-label="切换设备"]')?.value===value,id);
        };
        const tab = async (id, suffix) => page.locator(`a[href="/devices/${id}/${suffix}"]`).first().click();

        await page.goto(`${base}/devices`);
        await page.waitForURL('**/login');
        await page.locator('#username').fill('operator');
        await page.locator('#password').fill('test-only-password');
        await page.locator('form button[type="submit"]').click();
        await page.getByRole('heading', {name: '设备管理', exact: true}).waitFor();
        await text('USB A'); await text('MQTT B');
        const hiddenValue = async (value) => {
            assert.equal(await page.getByText(value, {exact:true}).count(), 0, 'Sensitive value is visible before reveal');
            assert.equal(await page.locator(`[title="${value}"]`).count(), 0, 'Sensitive value leaked through tooltip');
        };
        await hiddenValue('13800000001'); await hiddenValue('13800000002');
        await hiddenValue('123456789012340'); await hiddenValue('123456789012341');
        await page.getByRole('button', {name:'显示 USB A 手机号',exact:true}).click();
        await page.getByText('13800000001',{exact:true}).waitFor();
        await hiddenValue('13800000002');
        await page.getByRole('button', {name:'显示 USB A IMEI',exact:true}).click();
        await page.getByText('123456789012340',{exact:true}).waitFor();
        await hiddenValue('123456789012341');
        await page.getByRole('button', {name:'隐藏 USB A 手机号',exact:true}).click();
        await page.getByRole('button', {name:'隐藏 USB A IMEI',exact:true}).click();
        await hiddenValue('13800000001'); await hiddenValue('123456789012340');
        console.log('PASS device-card phone/IMEI default masking, independent reveal and hide');
        await page.screenshot({path: path.join(output, 'devices-desktop.png'), fullPage: true});
        console.log('PASS login gate and authenticated device list');

        for (const suffix of ['', '/messages', '/serial', '/notifications', '/scheduled-tasks', '/auto-flymode']) {
            const extra='?filter=unread%20only&return=%2Fdevices%2FdeviceA#details';
            await page.goto(`${base}/devices/deviceA${suffix}${extra}`);
            await page.getByLabel('切换设备').waitFor();
            await switchTo('deviceB');
            assert.equal(page.url(),`${base}/devices/deviceB${suffix}${extra}`,'Switch changed page/query/hash');
            await switchTo('deviceA');
            assert.equal(page.url(),`${base}/devices/deviceA${suffix}${extra}`);
        }
        await page.goto(`${base}/devices`);
        await text('USB A');
        console.log('PASS device switch preserves all six page routes, query parameters and URL hash');

        await page.getByRole('button', {name: '定位 USB A', exact: true}).click();
        await page.getByRole('button', {name: '停止定位 USB A', exact: true}).waitFor();
        assert.equal(records[0].status.locating, true);
        assert.equal(records[1].status.locating, false);
        assert(await page.getByRole('button', {name: '定位 Offline C', exact: true}).isDisabled());
        await page.getByRole('button', {name: '停止定位 USB A', exact: true}).click();
        await page.getByRole('button', {name: '定位 USB A', exact: true}).waitFor();
        assert.equal(records[0].status.locating, false);
        failLocate = true;
        await page.getByRole('button', {name: '定位 MQTT B', exact: true}).click();
        await text('MQTT B：定位灯操作失败');
        assert.equal(records[1].status.locating, false);
        failLocate = false;
        await page.getByRole('link', {name: 'MQTT B', exact: true}).click();
        await tab('deviceB', 'serial');
        await page.getByRole('button', {name: '定位 MQTT B', exact: true}).click();
        await page.getByRole('button', {name: '停止定位 MQTT B', exact: true}).waitFor();
        await page.getByRole('button', {name: '停止定位 MQTT B', exact: true}).click();
        await page.getByRole('button', {name: '定位 MQTT B', exact: true}).waitFor();
        await page.goto(`${base}/devices`);
        await text('USB A');
        records[1].features.locate_led = false;
        await page.getByRole('button', {name:'刷新',exact:true}).click();
        await delay(300);
        assert(await page.getByRole('button', {name:'定位 MQTT B',exact:true}).isDisabled());
        records[1].features.locate_led = true;
        await page.getByRole('button', {name:'刷新',exact:true}).click();
        assert(requests.filter((r)=>r.path.endsWith('/locate')).every((r)=>typeof r.body.enabled==='boolean'));
        console.log('PASS GPIO21 locator start/stop, device isolation, failure, offline and older-script states');

        await page.getByRole('link', {name: 'USB A', exact: true}).click();
        await page.getByRole('heading', {name: '设备概览', exact: true}).waitFor();
        await page.getByRole('button',{name:'显示 USB A 手机号',exact:true}).waitFor();
        await hiddenValue('13800000001');
        await page.getByRole('button',{name:'显示 USB A 手机号',exact:true}).click();
        await page.getByText('13800000001',{exact:true}).waitFor();
        await tab('deviceA','serial');
        await page.getByRole('button',{name:'显示 USB A 手机号',exact:true}).waitFor();
        await page.getByRole('button',{name:'显示 USB A 手机号',exact:true}).click();
        await page.getByText('13800000001',{exact:true}).waitFor();
        await switchTo('deviceB');
        await page.getByRole('button',{name:'显示 MQTT B 手机号',exact:true}).waitFor();
        await hiddenValue('13800000002');
        await switchTo('deviceA');
        await page.getByRole('button',{name:'显示 USB A 手机号',exact:true}).waitFor();
        await hiddenValue('13800000001');
        console.log('PASS dashboard/control masking and reset after device/page switch');
        await text('中国联通（SIM 推断）');
        await tab('deviceA','serial');
        await text('中国联通（SIM 推断）');
        await switchTo('deviceB');
        await text('测试运营商');
        await tab('deviceB','serial');
        await text('测试运营商');
        await page.goto(`${base}/devices/deviceB`);
        for (const [imsi, expected] of [['460031234567890','中国电信（SIM 推断）'],['460151234567890','中国广电（SIM 推断）'],['460001234567890','中国移动（SIM 推断）'],['530011234567890','One NZ（新西兰）（SIM 推断）'],['530041234567890','One NZ（新西兰）（SIM 推断）'],['310260123456789','未识别运营商'],['unknown','等待运营商信息']]) {
            records[0].status.mobile.imsi=imsi;
            await switchTo('deviceA');
            await text(expected);
            await switchTo('deviceB');
        }
        records[0].status.mobile.imsi='460011234567890';
        records[0].status.mobile.sim_ready=false;
        await switchTo('deviceA');
        await text('SIM 未就绪');
        await switchTo('deviceB');
        records[0].status.mobile.sim_ready=true;
        await switchTo('deviceA');
        console.log('PASS reported operator priority, SIM-network fallback, unknown and missing SIM handling');


        await tab('deviceA', 'messages');
        await text('只属于 deviceA 的短信');
        await page.getByText('10086', {exact: true}).first().click();
        await page.getByPlaceholder('输入消息内容...').fill('仅向 A 发送');
        delaySMS = true;
        const sent = page.waitForRequest((r) => r.url().endsWith('/devices/deviceA/serial/sms') && r.method() === 'POST');
        await page.locator('form button[type="submit"]').click(); await sent;
        await switchTo('deviceB');
        await tab('deviceB', 'messages'); await text('只属于 deviceB 的短信');
        await delay(1100);
        assert(page.url().endsWith('/devices/deviceB/messages'), 'late A mutation navigated B');
        assert(!(await page.locator('body').innerText()).includes('只属于 deviceA 的短信'));
        assert(requests.some((r) => r.path === '/devices/deviceA/serial/sms' && r.body.content === '仅向 A 发送'));
        console.log('PASS isolated messages and late command callbacks across device switch');

        delayAConversations = true;
        await switchTo('deviceA');
        const slowQuery = page.waitForRequest((r) => r.url().endsWith('/devices/deviceA/messages/conversations'));
        await tab('deviceA', 'messages'); await slowQuery;
        await switchTo('deviceB'); await tab('deviceB', 'messages');
        await text('只属于 deviceB 的短信'); await delay(1100);
        assert(!(await page.locator('body').innerText()).includes('只属于 deviceA 的短信'), 'late A query replaced B data');
        delayAConversations = false;
        console.log('PASS cancelled/stale queries cannot populate another device');

        await switchTo('deviceA'); await tab('deviceA', 'notifications');
        await page.getByPlaceholder('钉钉机器人 access_token').fill('UNSAVED_A_DRAFT');
        await switchTo('deviceB'); await tab('deviceB', 'notifications');
        await page.getByPlaceholder('钉钉机器人 access_token').waitFor();
        assert.equal(await page.getByPlaceholder('钉钉机器人 access_token').inputValue(), 'SAVED_deviceB');
        await page.getByPlaceholder('钉钉机器人 access_token').fill('B_UPDATED');
        await page.getByRole('button', {name: '保存配置', exact: true}).click();
        await text('通知渠道配置已保存');
        assert.equal(channels.deviceA[0].config.secretKey, 'SAVED_deviceA');
        assert.equal(channels.deviceB[0].config.secretKey, 'B_UPDATED');
        console.log('PASS device-specific form reset, notification save and 204 handling');

        await tab('deviceB', 'scheduled-tasks'); await text('任务-deviceB');
        await page.getByLabel('删除任务 任务-deviceB').click();
        await page.getByRole('dialog').getByRole('button', {name: /删除/}).click();
        await text('暂无计划任务');
        assert.equal(tasks.deviceA.length, 1);
        await switchTo('deviceA'); await tab('deviceA', 'scheduled-tasks');
        await page.getByRole('button', {name: '新建任务', exact: true}).first().click();
        await page.locator('#task-name').fill('A 独立任务');
        await page.locator('#task-phone').fill('10086');
        await page.locator('#task-content').fill('A 任务内容');
        await page.getByRole('dialog').getByRole('button', {name: '创建任务', exact: true}).click();
        await text('A 独立任务');
        assert.equal(tasks.deviceA.length, 2); assert.equal(tasks.deviceB.length, 0);
        console.log('PASS device-scoped scheduled task create and delete');

        await switchTo('deviceB'); await tab('deviceB', 'serial');
        await page.getByLabel('飞行模式自动恢复时间（秒）').fill('45');
        await page.getByRole('button', {name: '开启飞行模式', exact: true}).click();
        await text('飞行模式请求已提交');
        assert(requests.some((r) => r.path === '/devices/deviceB/serial/flymode' && r.body.duration_seconds === 45));
        await page.setViewportSize({width: 390, height: 844});
        assert(await page.evaluate(() => document.documentElement.scrollWidth <= innerWidth + 1), 'mobile page overflow');
        await page.screenshot({path: path.join(output, 'device-control-mobile.png'), fullPage: true});
        await page.setViewportSize({width: 1440, height: 1000});
        await tab('deviceB', 'auto-flymode'); await text('请使用定时恢复的飞行模式');
        await switchTo('deviceC'); await tab('deviceC', 'serial');
        assert(await page.getByRole('button', {name: '开启飞行模式', exact: true}).isDisabled());
        console.log('PASS MQTT timed restoration, mobile layout and offline controls');

        await page.locator('a[href="/devices/enroll"]').first().click();
        await page.locator('#enroll-device-id').fill('new-device');
        await page.getByRole('checkbox').check();
        await page.getByRole('button', {name: '生成注册配置', exact: true}).click();
        await text('配置已生成：new-device');
        const tokenField=page.locator('#registration-token');
        assert.equal(await tokenField.getAttribute('type'),'password');
        await page.getByRole('button',{name:'显示注册凭据',exact:true}).click();
        assert.equal(await tokenField.getAttribute('type'),'text');
        assert.equal(await tokenField.inputValue(),'OFFLINE_TEST_TOKEN_NOT_REAL_1234567890');
        await page.getByRole('button',{name:'隐藏注册凭据',exact:true}).click();
        assert.equal(await tokenField.getAttribute('type'),'password');
        await page.evaluate(() => {
            Object.defineProperty(navigator,'clipboard',{configurable:true,value:{writeText:async (value) => {window.__testCopiedToken=value;}}});
        });
        await page.getByRole('button',{name:'复制注册凭据',exact:true}).click();
        await text('注册凭据已复制');
        assert.equal(await page.evaluate(()=>window.__testCopiedToken),'OFFLINE_TEST_TOKEN_NOT_REAL_1234567890');
        await page.evaluate(() => {Object.defineProperty(navigator,'clipboard',{configurable:true,value:undefined});});
        await page.getByRole('button',{name:'复制注册凭据',exact:true}).click();
        await text('已选中注册凭据');
        assert.equal(await tokenField.getAttribute('type'),'text');
        assert(await tokenField.evaluate((input)=>input.selectionStart===0 && input.selectionEnd===input.value.length));
        assert.equal(requests.filter((r)=>r.path==='/devices/enroll').length,1,'Reveal/copy rotated credentials');
        const configDownloadPromise=page.waitForEvent('download');
        await page.getByRole('button',{name:'保存注册配置',exact:true}).click();
        const configDownload=await configDownloadPromise;
        const savedConfig=JSON.parse(await fs.readFile(await configDownload.path(),'utf8'));
        assert.equal(savedConfig.registrationToken,'OFFLINE_TEST_TOKEN_NOT_REAL_1234567890');
        await page.getByRole('button',{name:'生成注册配置',exact:true}).click();
        await text('配置已生成：new-device');
        assert.equal(await tokenField.getAttribute('type'),'password','New credential was revealed automatically');
        console.log('PASS enrollment token reveal/hide, copy, HTTP fallback and saved config');

        const downloadPromise = page.waitForEvent('download');
        await page.getByRole('button', {name: '下载公钥', exact: true}).click();
        const download = await downloadPromise;
        assert.equal(download.suggestedFilename(), 'server_public.pem');
        assert.equal(await fs.readFile(await download.path(), 'utf8'), publicKey);
        assert(!(await page.evaluate(() => JSON.stringify(localStorage))).includes('OFFLINE_TEST_TOKEN'));
        await page.locator('a[href="/devices"]').first().click();
        await page.locator('a[href="/devices/enroll"]').first().click();
        assert.equal(await page.locator('#registration-token').count(), 0, 'registration secret persisted after leaving page');
        await page.getByLabel('待撤销设备 ID').fill('new-device');
        await page.getByRole('button', {name: '撤销认证', exact: true}).click();
        await page.getByRole('button', {name: '确认撤销', exact: true}).click();
        await text('设备 MQTT 业务凭据已撤销');
        console.log('PASS enrollment, public key download, secret lifetime and revoke');

        await page.locator('a[href="/devices"]').first().click();
        failList = true;
        await page.getByRole('button', {name: '刷新', exact: true}).click();
        await text('列表暂时不可用');
        failList = false;
        await page.getByRole('button', {name: '重试', exact: true}).click(); await text('USB A');
        await page.getByRole('button', {name: '退出登录', exact: true}).click();
        await page.waitForURL('**/login');
        assert.equal(await page.evaluate(() => localStorage.getItem('token')), null);
        console.log('PASS load error recovery and logout');

        await page.goto(`${base}/oidc/callback?code=offline-code&state=offline-state`);
        await page.getByRole('heading', {name: '设备管理', exact: true}).waitFor();
        assert.equal(oidcExchanges, 1, 'OIDC code exchanged twice under StrictMode');
        assert.equal(unexpected.length, 0, `Unexpected/unscoped API calls: ${unexpected.join(', ')}`);
        assert.equal(errors.length, 0, `Runtime errors: ${errors.join('\n')}`);
        console.log('PASS OIDC StrictMode and no unscoped business requests');
        await context.close();
    } finally {
        if (browser) await browser.close();
        server.kill();
    }
}
main().catch((error) => {console.error(error); process.exitCode = 1;});
