# SMS 设备管理中心

基于合宙 Air780 系列模组的多设备短信管理系统，支持通过 **USB 串口** 或 **蜂窝 MQTT** 接入，由同一个 Web 后台管理设备、收发短信、配置通知转发和计划任务。

保留原项目蓝白配色、侧边导航、设备卡片和短信会话样式。登录后先选择设备，再进入对应功能；每台设备的数据、控制命令、转发配置和任务独立保存。

[原项目说明](https://blog.typesafe.cn/posts/air780e-giffgaff/) · [后端部署说明](BACKEND.md) · [Web 使用说明](WEB.md) · [Air780 脚本说明](AIR780.md)

**设备与固件**

- 新版已在 Air780EPM、`V2054_Air780EPM_106` 固件上进行串口和 MQTT 接入联调，脚本版本为 `2.1.1`。
- 原项目测试设备：Air780EHV、Air780EHM、Air780E、Air780EPV；其他型号请根据固件能力和接口兼容性实测。
- MQTT 模式需要固件支持 `mqtt`、`socket`、`rsa`、`crypto`、`fskv` 等模块，烧录时选择对应型号固件。

## 🌟 功能特性

- 多设备统一管理，支持 USB 串口和蜂窝 MQTT 设备同时接入。
- 账号密码登录，可选 OIDC；查看设备别名、在线状态、信号和 SIM 信息。
- 每台设备独立收发短信、保存记录、管理对话和接收来电通知。
- 支持钉钉、企业微信、飞书、自定义 Webhook、邮箱和 Telegram 通知；通知显示设备卡片设置的别名。
- 每台设备独立配置通知渠道、计划任务及串口自动飞行模式。
- GPIO21 LED 定位：从设备卡片或控制页开启闪烁，帮助查找目标板子。
- 手机号和 IMEI 默认隐藏、点击显示；顶部切换只显示别名，并保留当前功能页面。
- MQTT 用户名密码认证，可选 TLS；业务数据使用 RSA 公钥交换会话密钥和 AES-256-GCM 加密。
- 沿用 SQLite，保存设备、短信、配置、任务、命令结果和通知投递记录。

## 截图

以下截图展示原项目的界面风格，新版保留该风格并增加多设备管理入口。

![s1.png](screenshots/s1.png)
![s2.png](screenshots/s2.png)
![s3.png](screenshots/s3.png)
![s4.png](screenshots/s4.png)
![s5.png](screenshots/s5.png)

## 🚀 快速开始

----

**固件说明：部分型号或版本可能存在短信、网络或加密接口差异。请使用匹配型号的固件，并完成实际收发短信验证。**

**新版使用 `docker-compose.backend.yml` 从源码构建，不使用旧 `docker-compose.yml` 中的单设备预构建镜像。**

----

### 1. 硬件准备

**设备准备**：

- 插入有效 SIM 卡，连接天线，并保证供电稳定。
- USB 模式：将板子连接到运行后端的 Linux 宿主机。
- MQTT 模式：SIM 卡需有可用的蜂窝数据业务，模组需能访问 MQTT 服务器。

两种模式可在同一后台共存，但每台模组必须使用独立设备 ID，不要将同一 ID 复制给另一块板子。

### 2. 烧录 Lua 脚本

使用 [**LuaTools**](https://docs.openluat.com/air780epm/common/Luatools/) 下载匹配型号的固件和脚本。首次烧录选择「下载底层和脚本」。

通用脚本位于 [air780](air780/)，也可使用仓库根目录的同名文件：

```text
main.lua
device_config.lua
device_crypto.lua
device_mqtt.lua
device_commands.lua
```

同时下载以上文件，修改 `device_config.lua` 后重启模组。不要直接使用其他设备的私有配置。

![write.png](screenshots/write.png)

#### USB 串口模式

```lua
access_mode = "serial",
device_id = "",               -- 留空时自动使用本机 IMEI
device_name = "USB短信设备",
uart_id = uart.VUART_0,
heartbeat_ms = 60000,
```

串口模式不需要 MQTT 账号、公钥、TLS 证书或注册凭据。它不启动脚本中的 MQTT 连接，但不代表强制关闭模组底层数据网络。

#### 蜂窝 MQTT 模式

```lua
access_mode = "mqtt",
device_id = "",               -- 留空时自动使用本机 IMEI
device_name = "MQTT短信设备",
```

然后配置 `mqtt` 块中的域名、端口、用户名、密码和独立注册凭据，详见下方「MQTT 设备接入」。

### 3. 部署后台与 EMQX

在 Linux 宿主机安装 Docker 和 Docker Compose，然后下载源码：

```shell
git clone https://github.com/tmggg/uart_sms_forwarder.git
cd uart_sms_forwarder
cp config.example.yaml config.yaml
cp .env.example .env
cp deploy/emqx/users.csv.example deploy/emqx/users.csv
```

修改配置：

- `config.yaml`：设置 Web 登录密码的 bcrypt 哈希、随机 JWT Secret 和后端 MQTT 账号密码。
- `.env`：设置 EMQX 后台密码 `EMQX_DASHBOARD_PASSWORD` 和节点 cookie `EMQX_NODE_COOKIE`。cookie 生成一次后保持不变，可用 `openssl rand -hex 32` 生成。
- `deploy/emqx/users.csv`：替换示例后端账号密码。已知设备 ID 时可填写设备账号；暂时未知时删除设备占位行，稍后从 EMQX 后台新增。
- 后端 MQTT 账号密码必须与 EMQX 中的后端账号一致，默认后端用户名为 `sms_backend`。

启动服务：

```shell
docker compose -f docker-compose.backend.yml up -d --build
```

浏览器访问 `http://宿主机IP:8080`。模板默认映射 Web `8080`、MQTT `1883`，EMQX 容器名为 `sms_mqtt`。

EMQX 管理后台默认只绑定宿主机 `127.0.0.1:18083`，可在本机建立 SSH 隧道：

```shell
ssh -L 18084:127.0.0.1:18083 用户名@服务器IP
```

保持 SSH 连接，浏览器打开 `http://localhost:18084`。默认用户名为 `admin`，首次启动密码来自 `.env`。已有安装保留原来的端口映射和运行配置。

#### USB 设备接入

先在宿主机查找串口：

```shell
ls -l /dev/serial/by-id/ /dev/ttyACM* /dev/ttyUSB* 2>/dev/null
```

在 `docker-compose.backend.yml` 的后端服务下映射实际设备，例如：

```yaml
services:
  uart_sms_forwarder:
    devices:
      - /dev/ttyACM0:/dev/ttyACM0
```

优先将左侧替换为 `/dev/serial/by-id/...` 固定路径。板子可能提供多个 ACM 接口，需要映射脚本使用的 USB 虚拟串口；尚不确定时可逐条映射候选端口，由后端状态查询验证。

`config.yaml` 的串口路径填写容器内路径：

```yaml
App:
  Serial:
    Disabled: false
    Port: "/dev/ttyACM0"
    Ports: []
    ScanIntervalSeconds: 10
```

`Ports` 非空时优先于 `Port`；两者均为空时枚举容器可见串口。修改设备映射后重新创建后端容器：

```shell
docker compose -f docker-compose.backend.yml up -d --force-recreate --no-deps uart_sms_forwarder
```

验证成功后设备自动出现在 Web，无需 MQTT 注册。只使用串口时可设置 `App.MQTT.Enabled: false`；当前 Compose 仍会启动 EMQX 服务。

#### MQTT 设备接入

1. 登录 Web，在设备接入页输入设备 ID，生成独立注册凭据并下载服务器业务公钥。
2. 在 EMQX 后台为设备创建用户名密码。默认 ACL 要求用户名与设备 ID 一致；Web 发放注册凭据不会自动创建 EMQX 账号。
3. 在模组 `device_config.lua` 的 `mqtt` 块填写 `host`、`port`、`username`、`password`、`registration_token` 和 `key_id`。
4. 将业务公钥下载到模组 `/luadb/server_public.pem`，或填入 `public_key`。服务器私钥不下发设备。
5. 普通 MQTT 设置 `tls = false`；TLS 模式使用实际 TLS 端口，并将可信 CA 下载到 `/luadb/mqtt_ca.crt`。业务 RSA 公钥与 TLS CA 是不同文件，服务端 TLS 部署见 [BACKEND.md](BACKEND.md)。
6. 重启模组，MQTT 连接和后端业务认证均成功后，设备出现在 Web 列表。

`host` 必须是模组能访问的域名或 IP，不能填写 Docker 内部服务名 `sms_mqtt`。不同设备不要复用 `device_id` 或注册凭据。

### 4. 使用 Web 管理设备

- 登录后显示设备列表，选中设备进入概览、短信中心、设备控制、通知渠道和计划任务。
- 在设备卡片修改别名，通知使用后台保存的别名，无需重新下载脚本。
- 顶部切换设备时保留当前页面、查询参数和锚点，只替换设备 ID。
- GPIO21 定位灯每 500 毫秒切换亮灭；点击停止或重启模组后关闭，切换页面不会停止闪烁。
- 离线设备可查看历史和修改配置，控制命令需在线执行。

同一安装实例的登录账号共享设备管理权限，当前没有按用户划分设备归属的多租户功能。

### 5. 更新程序

先备份 `config.yaml`、`.env`、EMQX 认证和证书配置，以及 `data` 中的数据库和密钥。不要用示例覆盖已有账号、端口和密钥。

```shell
git pull --ff-only
docker compose -f docker-compose.backend.yml up -d --build
docker logs --tail 100 -f uart_sms_forwarder
```

后端将 Web 静态资源打包进二进制，修改前端后需重新构建镜像。更新模组脚本需通过 LuaTools 下载并重启。新版按全新部署设计，未提供旧单设备数据库的迁移流程。

## ⚙️ 常用配置

### 修改 Web 登录密码

在服务器用 `htpasswd` 交互式生成 bcrypt 哈希；没有该工具时先安装系统对应的 Apache 工具包：

```shell
htpasswd -nB -C 12 admin
```

将输出中 `admin:` 后的完整哈希填入 `config.yaml` 的 `App.Users.admin`，保存后重新创建后端容器。这里修改的是 Web 登录密码，不是 EMQX 后台或 MQTT 设备密码。

### 数据更新间隔

| 项目 | 默认间隔 |
|---|---|
| 后端发现新串口 | 10 秒，通过 `ScanIntervalSeconds` 修改 |
| 已连接串口状态查询 | 约 10 秒，后端发送 `get_status` |
| MQTT 设备状态心跳 | 60 秒，通过脚本 `heartbeat_ms` 修改 |
| 短信、来电事件 | 事件触发后上报，不等待心跳 |
| Web 设备列表和状态 | 10 秒，读取后端保存的状态 |
| Web 短信列表和当前对话 | 5 秒 |
| MQTT 无数据离线阈值 | 180 秒，通过 `OfflineSeconds` 修改，后端每 10 秒检查 |

### 运营商与手机号

设备上报运营商名称时优先使用，否则按 IMSI 网络前缀推断 SIM 归属运营商。目前内置中国大陆及 One NZ 对应代码，未接入公共运营商查询 API。SIM 归属运营商不一定是当前漫游网络；部分 SIM 未保存本机号码，手机号可能为空。

## 📝 新版更新说明

### 2026-10-25 · 多设备版本 / Air780 脚本 2.1.1

- 从单设备升级为多设备 Web，增加设备列表、别名、注册凭据发放和设备级路由。
- 增加 USB 自动发现及蜂窝 MQTT 接入，支持 EMQX Docker 部署、可选 TLS 和业务报文加密。
- 按设备隔离短信、通知、计划任务、控制命令和状态，保留原有短信与串口控制功能。
- 增加 GPIO21 定位、手机号与 IMEI 点击显示、通知设备别名及 One NZ 识别。
- 修复 `+86` 对话读取与删除、MQTT 发布参数和特定固件 GCM 标签校验兼容问题。
- 修复心跳未更新开机时长，脚本每次心跳携带最新 `mobile` 信息。
- Web 名称统一为「SMS 设备管理中心」，切换设备保留当前操作页面。

详细实现、限制和测试方法见 [BACKEND.md](BACKEND.md)、[WEB.md](WEB.md) 和 [AIR780.md](AIR780.md)。
