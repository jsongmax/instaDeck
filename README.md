# InstaDeck

InstaDeck 是一个自托管的管理面板，用来在一个页面里统一管理多个 [InstaBox](https://instabox.sh) 账号的云端 Linux 机器，不用来回切换账号。

> **项目处于早期开发阶段。** 第一版已经可以在本地运行，但只用模拟的 InstaBox 服务测试过，尚未用真实账号验证，部署配置也没有实际运行过。

本项目是独立的第三方工具，与 InstaBox 官方无关。使用多个账号前，请自行确认符合 InstaBox 的服务条款。

## 计划功能

面板功能包括：

- **账号管理：** 添加 API Key，查看每个账号的用量和配额。
- **统一机器列表：** 跨账号显示所有机器，可按账号和状态筛选。单个账号出错不影响其他账号。
- **机器操作：** 启动、停止、暂停、恢复、创建和删除，并显示操作进度。
- **网页终端：** 在面板里直接打开机器的终端。
- **端口：** 发布 HTTP / TCP 端口、私有访问和一次性令牌轮换。
- **快照：** 跨账号查看、保存、删除快照，并从快照新建机器。
- **操作记录：** 按账号、结果和事件类型筛选，导出 CSV。
- **通知渠道：** Telegram 和签名 Webhook，订阅账号失效、操作失败与机器异常。

## 开发进度

- [x] 安全底座：配置、管理员登录和两步验证、会话、密钥加密、审计事件模型
- [x] SQLite 存储和 InstaBox API 客户端
- [x] 账号管理和跨账号机器聚合
- [x] 机器操作和网页终端
- [x] HTTP 接口、管理命令和部署配置（部署配置尚未实际验证）
- [x] 前端界面（机器、终端、账号、审计、安全设置、外观主题）
- [x] 零配置启动和网页首次设置向导
- [x] 用真实 InstaBox 账号本地验证
- [ ] 部署到服务器并经 Cloudflare Tunnel 验证
- [x] 第二阶段：端口、快照、操作日志筛选与 CSV 导出、Telegram / Webhook 通知（假服务测试）
- [ ] 第二阶段功能用真实接口验证
- [ ] 后续：文件管理、远程桌面、定时保活

第二阶段的完整改动文件、测试结果和未验证项见[实现与验证记录](docs/phase-two-review.md)。

## 技术栈

- **后端：** Go、chi、SQLite（`modernc.org/sqlite`）
- **前端：** React、Vite、TypeScript、TanStack Query、xterm.js
- **部署：** Docker 或单一可执行文件，由 Cloudflare Tunnel / Caddy 提供 HTTPS

## 安全设计

这个面板集中保存了多个账号的 API Key，一旦被攻破，所有已接入的账号都会受影响。因此设计上：

- 管理员登录必须通过密码和 TOTP 两步验证。
- 默认只监听回环地址；Docker 显式启用私网代理信任，由 Cloudflare Tunnel 提供 HTTPS。
- API Key 用 AES-256-GCM 加密存储，主密钥放在独立文件中，Key 不会发送到浏览器。

本地加密只能防止数据库文件泄露，无法防止服务器本身被攻破。建议只给 API Key 必需的权限，并在怀疑泄露时立即到 InstaBox 吊销。

快照功能的 API Key 需要 `images:read`、`images:write`、`images:delete`（即相应的 `images:*` 权限）；机器权限不能替代快照权限。保存快照可能包含磁盘中的凭据与文件，请妥善管理；从快照新建机器不会修改原机器。

私有端口的令牌只在发布或轮换时显示一次，关闭弹窗前请复制。公开端口的网址可被任何持有它的人访问。面板接受端口 1–65535，但当前 OpenAPI 的发布下限为 1024，低端口仍可能被上游拒绝。

通知配置使用主密钥加密，`admin rotate-key` 同时轮换通知配置；请连同 API Key 一样备份主密钥。Webhook 只允许 HTTPS，不跟随重定向。“拦截内网地址”同时作用于终端 SSH 和 Webhook，默认开启。机器监测仅在有启用渠道订阅机器事件时每 60 秒读取现有 fleet 服务；首次建立基线，面板操作进行中及结束后两分钟不发送停止通知，同一机器同一事件十分钟内去重。发送队列最多 64 条，满时丢弃并记录固定类别日志，每条最多尝试三次，通知不保证送达。

## 快速开始

构建前端和程序后，直接运行：

```bash
instadeck serve
```

打开 http://127.0.0.1:8080，按向导创建管理员、扫描二维码绑定两步验证，并保存恢复码。无需预先配置环境变量或用命令行生成主密钥。

不用 Docker 时，可选使用 `deploy/instadeck.service` 和 `deploy/Caddyfile`：安装程序并创建服务用户后，只需修改 Caddyfile 的域名，由 Caddy 提供 HTTPS。首次设置码在服务日志中查看：`journalctl -u instadeck`，用于防止他人抢先设置。

默认数据库 `panel.db` 和主密钥 `master.key` 都在当前目录的 `instadeck-data` 中；systemd 部署使用 `/var/lib/instadeck`。请备份数据库，并单独保管主密钥；主密钥丢失后无法解密 API Key。`admin backup` 仅备份数据库。全部环境变量都是可选覆盖，参见 `deploy/.env.example`。

更换域名可运行 `instadeck admin set-origin https://新域名`，然后重启服务；环境变量 `INSTADECK_ORIGIN` 优先于保存的网址。管理命令须使用与服务相同的数据目录。`admin init` 保留作备用；只有验证器和恢复码都丢失时，使用 `admin reset-auth` 重置认证，随后重启服务。

## Docker 和 Cloudflare Tunnel 部署

镜像由 GitHub Actions 自动构建并发布到 `ghcr.io/jsongmax/instadeck`，支持 amd64 和 arm64，服务器上不需要编译。下面假设 cloudflared 已经装在宿主机上。

1. 把仓库的 `docker-compose.yml` 放到服务器上，在同一目录运行：

   ```bash
   docker compose pull
   docker compose up -d
   ```

   不需要任何配置，主密钥会自动生成，管理员由首次向导创建。端口只发布到宿主机回环，默认 8080，可在 `.env` 里用 `INSTADECK_PORT` 修改。

2. 在 Cloudflare Zero Trust 中给 Tunnel 配置 Public Hostname，例如 `panel.example.com`，服务地址填 `http://127.0.0.1:8080`。不要改写 HTTP Host Header。

3. **用公网域名 `https://panel.example.com` 打开面板完成向导**。首次设置码用下面的命令查看，每次重启都会换新码，以最后一条为准：

   ```bash
   docker compose logs instadeck
   ```

   向导会把当前网址保存为面板地址。如果在 `http://127.0.0.1:8080` 上完成设置，之后公网域名会被拒绝，修复方式：

   ```bash
   docker compose exec instadeck /usr/local/bin/instadeck admin set-origin https://新域名
   docker compose restart instadeck
   ```

4. Cloudflare 侧关闭 Rocket Loader（会被 CSP 拦截）；建议增加 Cloudflare Access 作为第二道门。免费和 Pro 计划单次请求上限为 100 MB；WebSocket 默认开启，面板会发送心跳维持空闲连接，终端仍受 30 分钟空闲超时约束。

5. 备份数据卷中的 `panel.db` 和 `master.key`，两者分开保管。主密钥丢失后 API Key 无法解密。为保证数据库一致，可先停止面板再复制：

   ```bash
   docker compose stop instadeck
   docker compose cp instadeck:/data/panel.db ./panel.db
   docker compose cp instadeck:/data/master.key ./master.key
   docker compose start instadeck
   ```

6. 升级：

   ```bash
   docker compose pull
   docker compose up -d
   ```

   `latest` 跟随 `main` 分支。正式使用建议在 `.env` 里用 `INSTADECK_TAG` 固定版本标签，例如 `INSTADECK_TAG=1.0.0`。打 `v1.0.0` 这样的 Git 标签会发布对应版本的镜像。

**从源码构建：** 在仓库根目录运行 `docker compose up -d --build`。

**想让 cloudflared 也跑在 Compose 里：** 在 `.env` 里写 `CLOUDFLARE_TUNNEL_TOKEN=你的令牌`，启动时加 `--profile tunnel`，服务地址改成 `http://instadeck:8080`。

镜像以非 root 用户运行，数据库和主密钥只写入 `/data` 命名卷。镜像内置 `INSTADECK_TRUSTED_PROXY=private`，只信任回环和私网对端的 X-Forwarded-For 最右一跳，请不要把面板端口直接发布到公网。不用 Docker 时默认只允许回环监听，Caddy 和 systemd 是可选方式。

## 开发

需要 Go 1.26 或更高版本。

```bash
go test ./internal/...
```

## 许可证

暂未选择许可证。
