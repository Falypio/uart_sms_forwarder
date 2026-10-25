# 多设备后端（全新部署）

Go 后端、部署配置及多设备 Web 已对接。Web 登录后从设备列表进入设备级页面，使用 `/api/devices/{deviceId}` 业务接口，详见 WEB.md。旧的无设备 ID 的业务路由已停用，避免误操作其他设备；登录、OIDC 和版本接口保留。

## 已实现

- 沿用 SQLite，直接初始化新表，无旧数据迁移。设备、短信、定时任务、配置、命令记录、会话和事件确认都绑定稳定设备 ID。
- 服务启动后扫描容器可见的所有串口，之后默认每10秒扫描；发送 get_status 验证新版 Lua 协议及 device_id 后登记设备。每个串口独立连接、读写和重连；未知串口不登记，不以端口名当设备身份。
- MQTT 用户名密码连接，可选 TLS；仅订阅 register/events/results/status，拒绝 retained 数据和超大报文。设备注册另校验独立注册凭据，保存模组/SIM特征；列表只包含实际接入并认证过的设备。
- 完整对接 `air780/device_crypto.lua` 的 RSA-PKCS1-v1_5 + HMAC-SHA256 派生 + AES-256-GCM 协议，详见 AIR780.md。上下行序号持久化；nonce 在发布之前分配并落盘，发布失败也不复用。
- 后端重启保留密钥和数据库后，可继续处理原会话上报。旧/退休 sid 不得重新激活，重复序号拒绝，跨会话 message_id 去重；保存事件后才发送业务 ACK。
- 所有控制按指定设备路由。MQTT 飞行模式必须提供恢复秒数；串口自动飞行模式按设备配置。自动定时任务每日本地时间8点起检查并补执行，按 IntervalDays 判断，发送最终结果回写任务。
- 通知渠道按设备保存。入库事件和通知任务在同一事务提交，通知失败指数退避重试最多8次。通知是至少一次投递，外部渠道成功后落盘前崩溃仍可能重复，需要接收端自行幂等。
- 已登录账号是同一安装实例的可信运维人员，共享设备列表；本轮没有引入多租户/每用户设备归属。设备隔离不是租户权限隔离。

## 部署步骤

1. 复制 `config.example.yaml` 为 `config.yaml`，更改 Web 登录密码哈希、JWT Secret 和 MQTT 后端密码。
2. 复制 `deploy/emqx/users.csv.example` 为 `deploy/emqx/users.csv`，把示例占位值全部替换。后端账号固定 `sms_backend`，设备账号名取其 device_id，密码独立。EMQX 内置数据库在首次创建认证器时导入文件；后续账号变更通过 Dashboard 的认证用户管理完成，修改 CSV 不会覆盖已存在账号。
3. 新部署复制 `.env.example` 为 `.env`，设置 `EMQX_DASHBOARD_PASSWORD` 和 `EMQX_NODE_COOKIE`；两者均替换为真实强随机值，例如用 `openssl rand -hex 32` 生成节点 cookie。已有 `.env` 不要覆盖，沿用已有 cookie；若缺少此变量，将当前实际使用的节点 cookie 保存进去。节点 cookie 供 EMQX 内部使用，不是设备 MQTT 密码。真实 `.env` 已加入忽略规则。
4. 在 `docker-compose.backend.yml` 的 devices 中映射实际 USB 设备。优先用宿主机 `/dev/serial/by-id/...` 映射到固定容器路径，多个设备逐条映射。后端只扫描容器可见端口；Docker 静态映射不能保证新增/拔插设备后自动出现，必要时重新创建容器。无需串口时设置 `App.Serial.Disabled=true`。
5. 由用户执行：`docker compose -f docker-compose.backend.yml up -d --build`。此配置从当前源码构建，**不要使用旧 docker-compose.yml 的预构建单设备镜像**。MQTT 服务及容器名均为 `sms_mqtt`。1883对外，Dashboard默认只绑定宿主机127.0.0.1:18083，可通过SSH隧道管理。
6. 备份 `data/app.db` 和 `data/keys/server_private.pem`，不要单独删除密钥文件。新部署默认生成2048位 RSA 私钥，权限0600。数据库含短信和渠道配置，卷访问权限按部署环境管理。

默认运行单个后端实例。SQLite 连接数设为1以序列化写入，不支持多副本并发调度和跨实例连接路由。

### EMQX 基础配置与更新

`deploy/emqx/emqx.conf` 会覆盖镜像原文件，因此明确保留 `node.name`、`node.data_dir`，并启用 `dashboard.listeners.http.bind = "0.0.0.0:18083"`。Compose 通过 `EMQX_NODE__COOKIE` 注入 `.env` 中的节点 cookie；缺少 cookie 会在创建容器前明确报错。镜像版本固定为 5.8.8。

容器内部始终使用 MQTT 1883、Dashboard 18083。服务器现有的 `10086:1883`、`18084:18083`、Web `9090:8080` 是宿主机映射，上传代码时保留服务器自己的端口配置。仓库模板默认仍为 MQTT 1883、仅本机访问的 Dashboard 18083 和 Web 8080。更新时先备份服务器的 Compose、`.env`、`config.yaml`、`users.csv` 与证书；不要用模板覆盖已配置的密码、账号、证书和端口。

只修改 `emqx.conf` 后，重启 `sms_mqtt` 使其生效；修改 Compose 环境变量则需重新创建容器。需要启动/应用配置时执行：

```sh
docker compose -f docker-compose.backend.yml up -d --no-deps --force-recreate sms_mqtt
docker logs --tail 80 sms_mqtt
```

日志应出现 MQTT TCP 1883 和 HTTP Dashboard 18083 的监听启动记录；从宿主机映射端口访问。重新创建容器会保留命名数据卷，不需要删除卷或清空认证数据库。

### 可选 TLS

- 在 `deploy/emqx/certs` 放入真实的 server.crt（服务器证书链）、server.key（对应私钥）、ca.crt（客户端信任的 CA）。证书 SAN 要包含后端使用的主机名（例如 sms_mqtt）以及蜂窝设备访问的公网域名，或分别使用正确证书名连接。
- `config.yaml` 设置 Broker=`ssl://sms_mqtt:8883`、TLS=true、CAFile=`/app/mqtt-ca.crt`。
- 运行 `docker compose -f docker-compose.backend.yml -f docker-compose.tls.yml up -d --build`。仍使用用户名密码认证，不要求客户端证书。overlay 默认保留1883；若只允许 TLS，另关闭TCP监听/端口。
- 设备端使用可访问的公网域名/IP，不能填 Docker 内部名称 sms_mqtt；烧录 CA 和服务端业务公钥，按 AIR780.md 配置。业务 RSA 公钥和 TLS CA 是不同文件。

## 首次设备登记

先登录：`POST /api/login`，请求 `{"username":"admin","password":"你的密码"}`。之后所有 API 带 `Authorization: Bearer <token>`。

1. `POST /api/devices/enroll`，请求 `{"deviceId":"设备IMEI或脚本配置的ID"}`。
2. 响应包含 deviceId、registrationToken（只返回这一次）、keyId、publicKey、topicPrefix。后端数据库只保存注册令牌的 SHA-256。重新调用会轮换令牌并退休现有 MQTT 会话。
3. publicKey 保存成 `server_public.pem`；将设备专属 registrationToken、keyId 和 EMQX用户名密码填入 `air780/device_config.lua`，连同全部脚本/公钥烧录。
4. 在 EMQX 内置认证库创建同 device_id 的用户名及独立密码；Broker账号和后端注册凭据是两层认证，**enroll API 不会自动创建 EMQX 用户**。
5. Lua 连接后发布注册请求，后端验证并返回加密 register_result，此时设备出现在 `GET /api/devices`。USB 模式通过本地可信串口身份握手登记，无需 MQTT 凭据。

撤销业务注册：`POST /api/device-enrollments/{deviceId}/revoke`，现有会话随即拒绝；还应在 EMQX 单独禁用该账号。只撤销业务凭据不会删除 Broker 账号。

同一 device_id 同时只使用一种接入方式；活跃 USB 连接不会被 MQTT 注册抢占。设备换卡不换 ID；同时连接的不同模组必须使用不同 ID。

## API 清单

认证与全局：

| 方法 | 路径 | 用途 |
|---|---|---|
| GET | /api/devices | 已认证设备列表、在线状态、特征及状态快照 |
| POST | /api/devices/enroll | 发放/轮换 MQTT 业务注册凭据 |
| GET | /api/devices/server-public-key | 获取业务公钥和 keyId |
| POST | /api/device-enrollments/{deviceId}/revoke | 撤销 MQTT 业务认证 |

以下均以 `/api/devices/{deviceId}` 为前缀：

| 方法 | 后缀 | 用途 |
|---|---|---|
| GET / PUT | 空后缀 | 详情 / 修改名称 |
| GET | /status 或 /serial/status | 设备独立状态 |
| POST | /commands | 通用控制，JSON带action及参数 |
| GET | /commands、/commands/{id} | 最近200条命令 / 单条命令 |
| POST | /serial/sms | to、content |
| POST | /serial/flymode | enabled；MQTT启用时需duration_seconds |
| POST | /serial/reboot、/serial/reset-stack | 重启 / 重置协议栈 |
| POST | /locate | enabled 布尔值，控制 GPIO21 持续闪烁 / 停止，结果由设备回报 |
| GET | /messages/stats | 设备短信统计 |
| GET | /messages/conversations | 设备会话列表 |
| GET | /messages/conversations/{peer}/messages | 指定号码短信 |
| DELETE | /messages、/messages/{id}、/messages/conversations/{peer} | 仅删除所选设备记录 |
| GET / PUT | /properties/{id} | notification_channels、auto_flymode_config |
| POST | /notifications/{type}/test | 测试所选设备渠道 |
| GET | /notification-deliveries | 最近通知任务状态，不返回渠道密码 |
| GET / POST | /scheduled-tasks | 任务列表 / 创建 |
| GET / PUT / DELETE | /scheduled-tasks/{id} | 查询 / 修改 / 删除 |
| POST | /scheduled-tasks/{id}/trigger | 手动执行该设备任务 |

控制接口返回202和命令记录，表示请求进入处理，**不表示短信已经发送成功**。查询命令状态：pending → accepted/queued → succeeded/failed；超时、发送中断等标记 unknown，不自动重发付费短信。请求结果不确定时需要人工核对，再决定另发新命令；同一任务若前次仍pending或unknown，不允许自动重复发送。飞行模式/重启会断开设备，应结合重新上线状态判断，不把 accepted 当作完成。

通知渠道字段沿用原项目，Telegram使用 apiToken、userid 和可选代理配置；每台设备同类型渠道只配置一次。重试时读取该设备最新渠道配置，禁用或删除渠道会取消尚未执行的发送，已在途请求无法撤回。每台设备默认无通知渠道、自动飞行模式关闭。MQTT不允许无定时恢复的自动飞行模式；手动操作必须传duration_seconds。

## 验证与用户联调

离线：`go test ./...`、`go vet ./...`，编译 Go 前需在web目录构建静态文件（`npm ci && npm run build`）；源码Dockerfile会执行该步骤。

新增测试覆盖登录门槛、设备数据/配置/命令/通知隔离、Lua生成的加密测试向量、注册失败、篡改、旧会话与跨重启重放、事件重复入库、串口分包和探测同批短信。测试不连接真实串口或 Broker。

用户联调：EMQX账号/ACL和可选TLS、真实SIM短信与来电、多个USB端口、Docker映射及拔插、MQTT断网恢复/ACK、设备独立转发、定时任务和控制结果。当前设备端 outbox 仍为有界内存缓存，不保证掉电保存；本轮没有偷偷修改Lua协议或设备脚本。
