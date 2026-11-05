# InstaDeck

[![CI](https://github.com/jsongmax/instaDeck/actions/workflows/ci.yml/badge.svg)](https://github.com/jsongmax/instaDeck/actions/workflows/ci.yml)
[![Release](https://img.shields.io/github/v/release/jsongmax/instaDeck?include_prereleases)](https://github.com/jsongmax/instaDeck/releases)

InstaDeck 是一个自托管的管理面板，用来在一个页面里统一管理多个 [InstaBox](https://instabox.sh) 账号的云端 Linux 机器，不用来回切换账号。

本项目是独立的第三方工具，与 InstaBox 官方无关。使用多个账号前，请自行确认符合 InstaBox 的服务条款。

> **项目处于早期阶段。** 机器管理已用真实账号在本地验证；其他功能的验证情况见[当前状态](#当前状态)。

## 目录

- [功能](#功能)
- [快速开始](#快速开始)
- [部署到服务器](#部署到服务器)
- [API Key 权限](#api-key-权限)
- [配置](#配置)
- [备份与恢复](#备份与恢复)
- [管理命令](#管理命令)
- [安全设计](#安全设计)
- [当前状态](#当前状态)
- [开发](#开发)
- [发布新版本](#发布新版本)
- [许可证](#许可证)

## 功能

- **统一机器列表：** 跨账号显示所有机器，可按账号和状态筛选。单个账号出错不影响其他账号。
- **机器操作：** 创建、启动、停止、暂停、恢复、重命名、删除，提交后立即显示进度。
- **网页终端：** 在面板里直接打开机器的终端，可同时开多个窗口。
- **账号管理：** 添加和替换 API Key，查看每个账号的用量和配额。
- **端口：** 发布和编辑 HTTP / TCP 端口，支持私有访问和令牌轮换。
- **快照：** 跨账号查看、保存、删除快照，并从快照新建机器。
- **操作记录：** 按账号、结果和事件类型筛选，可导出 CSV。
- **通知：** 通过 Telegram 或带签名的 Webhook 接收账号失效、操作失败和机器异常的通知。
- **登录保护：** 密码加 TOTP 两步验证、恢复码、会话管理，敏感操作可要求再次验证。
- **零配置：** 无需预先设置环境变量，首次打开时由网页向导完成设置。

## 快速开始

在自己的电脑上试用，只需要下载一个文件。

1. 从 [Releases](https://github.com/jsongmax/instaDeck/releases) 下载对应系统的压缩包并解压。支持 Linux、macOS（amd64 / arm64）和 Windows（amd64）。

2. 运行：

   ```bash
   ./instadeck serve
   ```

3. 打开 http://127.0.0.1:8080，按向导创建管理员、绑定两步验证，并保存恢复码。

4. 到“InstaBox 账号”页面添加 API Key。

数据默认保存在当前目录的 `instadeck-data` 中，以后要从同一目录启动。

## 部署到服务器

面板必须经 HTTPS 对外提供访问。推荐用 Docker 加 Cloudflare Tunnel。

### Docker 和 Cloudflare Tunnel

镜像由 GitHub Actions 自动构建并发布到 `ghcr.io/jsongmax/instadeck`，支持 amd64 和 arm64，服务器上不需要编译。下面假设 cloudflared 已经装在宿主机上。

1. 把仓库的 `docker-compose.yml` 放到服务器上，在同一目录运行：

   ```bash
   docker compose pull
   docker compose up -d
   ```

   不需要任何配置，主密钥会自动生成，管理员由首次向导创建。端口只发布到宿主机回环，默认 8080，可在 `.env` 里用 `INSTADECK_PORT` 修改。

2. 在 Cloudflare Zero Trust 中给 Tunnel 配置 Public Hostname，例如 `panel.example.com`，服务地址填 `http://127.0.0.1:8080`。不要改写 HTTP Host Header。

3. **用公网域名 `https://panel.example.com` 打开面板完成向导。** 首次设置码用下面的命令查看，每次重启都会换新码，以最后一条为准：

   ```bash
   docker compose logs instadeck
   ```

   向导会把当前网址保存为面板地址。如果在 `http://127.0.0.1:8080` 上完成了设置，之后公网域名会被拒绝，修复方式：

   ```bash
   docker compose exec instadeck /usr/local/bin/instadeck admin set-origin https://新域名
   docker compose restart instadeck
   ```

4. 在 Cloudflare 侧关闭 Rocket Loader（会被 CSP 拦截）。建议增加 Cloudflare Access 作为第二道门。

**升级：**

```bash
docker compose pull
docker compose up -d
```

`latest` 跟随 `main` 分支。正式使用建议在 `.env` 里固定版本，例如 `INSTADECK_TAG=1.0.0`。

**从源码构建：** 在仓库根目录运行 `docker compose up -d --build`。

**让 cloudflared 也跑在 Compose 里：** 在 `.env` 里写 `CLOUDFLARE_TUNNEL_TOKEN=你的令牌`，启动时加 `--profile tunnel`，服务地址改成 `http://instadeck:8080`。

镜像以非 root 用户运行，数据只写入 `/data` 命名卷。镜像内置 `INSTADECK_TRUSTED_PROXY=private`，只信任回环和私网对端转发的来源地址，所以不要把面板端口直接发布到公网。

Cloudflare 的免费和 Pro 计划单次请求上限为 100 MB。终端连接受 30 分钟空闲超时约束，面板会发送心跳维持空闲连接。

### systemd 和 Caddy

不用 Docker 时，可以使用发布包里 `deploy/` 目录下的文件：

1. 创建 `instadeck` 服务用户，把程序放到 `/opt/instadeck/instadeck`。
2. 安装 `deploy/instadeck.service`，数据保存在 `/var/lib/instadeck`。
3. 把 `deploy/Caddyfile` 里的域名改成自己的，由 Caddy 提供 HTTPS。
4. 启动服务，用 `journalctl -u instadeck` 查看首次设置码，再用公网域名打开面板完成向导。

## API Key 权限

只给 Key 必需的权限。使用全部功能时，可以直接用 Full Access。

| 功能 | 需要的权限 |
|---|---|
| 查看机器和用量 | `machines:read` |
| 创建、启停、重命名机器，打开终端 | `machines:write` |
| 删除机器 | `machines:delete` |
| 查看、保存、删除快照 | `images:read`、`images:write`、`images:delete` |

机器权限不能代替快照权限。InstaBox 的接口文档没有写明端口操作需要的权限；Full Access 可以读取端口，其他组合未验证。

InstaBox 不提供查询 Key 权限的接口，所以添加账号时要由你声明这个 Key 是否有写入和删除权限。

## 配置

全部配置都是可选的。常用的环境变量如下，完整示例见 [`deploy/.env.example`](deploy/.env.example)。

| 变量 | 默认值 | 说明 |
|---|---|---|
| `INSTADECK_DATA_DIR` | `instadeck-data` | 数据目录。Docker 中为 `/data` |
| `INSTADECK_DATABASE` | `<数据目录>/panel.db` | 数据库文件 |
| `INSTADECK_MASTER_KEY_FILE` | `<数据目录>/master.key` | 主密钥文件 |
| `INSTADECK_LISTEN` | `127.0.0.1:8080` | 监听地址。监听非回环地址时必须同时设置下一项 |
| `INSTADECK_TRUSTED_PROXY` | 空 | 设为 `private` 时，信任回环和私网代理转发的来源地址。Docker 镜像已内置 |
| `INSTADECK_ORIGIN` | 向导保存的网址 | 面板公网地址，优先于保存的网址 |
| `INSTADECK_TAG` | `latest` | 仅 Compose 使用：镜像版本 |
| `INSTADECK_PORT` | `8080` | 仅 Compose 使用：宿主机回环端口 |
| `CLOUDFLARE_TUNNEL_TOKEN` | 空 | 仅 Compose 的 `tunnel` profile 使用 |

面板内的“登录与安全”页面还有两个开关，都默认开启：

- **拦截内网地址：** 终端 SSH 和 Webhook 不连接内网地址，防止面板被诱导访问服务器所在的内部网络。服务器部署时请保持开启。如果本机代理使用 fake-IP，这个开关可能拦截正常连接。
- **敏感操作再次验证：** 删除、添加 Key、发布端口等操作前，要求重新输入密码和验证码，通过后 5 分钟内不再询问。

## 备份与恢复

需要备份两个文件，**分开保管**：

- `panel.db`：数据库。
- `master.key`：主密钥。**丢失后已保存的 API Key 和通知配置都无法解密。**

Docker 部署时，先停止面板再复制，以保证数据库一致：

```bash
docker compose stop instadeck
docker compose cp instadeck:/data/panel.db ./panel.db
docker compose cp instadeck:/data/master.key ./master.key
docker compose start instadeck
```

不用 Docker 时，可以在运行中用 `instadeck admin backup <目标文件>` 备份数据库。这个命令不包含主密钥。

恢复时把两个文件放回数据目录，再启动面板。恢复流程尚未实际演练过，请先在测试环境试一次。

## 管理命令

管理命令必须使用与服务相同的数据目录。

| 命令 | 作用 |
|---|---|
| `instadeck serve` | 启动面板 |
| `instadeck version` | 显示版本号 |
| `instadeck healthcheck` | 检查运行中的面板是否正常 |
| `instadeck admin set-origin <url>` | 更换面板网址，之后需要重启 |
| `instadeck admin backup <目标文件>` | 备份数据库 |
| `instadeck admin rotate-key` | 轮换主密钥，并重新加密 API Key 和通知配置 |
| `instadeck admin reset-auth [用户名]` | 验证器和恢复码都丢失时重置认证，之后需要重启 |
| `instadeck admin init [用户名]` | 用命令行创建管理员（备用，通常由向导完成） |
| `instadeck admin generate-key` | 生成一份主密钥（备用，通常自动生成） |

## 安全设计

这个面板集中保存了多个账号的 API Key，一旦被攻破，所有已接入的账号都会受影响。

- **登录：** 必须通过密码和 TOTP 两步验证。会话空闲 30 分钟或满 8 小时后失效。
- **网络：** 默认只监听回环地址。未登录时，所有业务接口和终端连接都不可访问。
- **密钥：** API Key 用 AES-256-GCM 加密存储，主密钥放在独立文件中，Key 不会发送到浏览器。
- **终端：** 每次会话使用内存中的临时密钥，校验服务器身份，不记录终端内容。
- **Webhook：** 只允许 HTTPS，不跟随重定向，并附带 HMAC 签名。
- **审计：** 账号和机器操作都会写入操作记录，失败时记录错误码。

本地加密只能防止数据库文件泄露，无法防止服务器本身被攻破。如果怀疑泄露，请立即到 InstaBox 吊销 Key。

使用时请注意：

- 快照可能包含磁盘中的凭据和文件，请妥善管理。
- 私有端口的令牌只在发布、改为私有或轮换时显示一次，关闭弹窗前请复制。
- 公开端口的网址可被任何持有它的人访问。
- 通知不保证送达：发送队列最多 64 条，满时丢弃，每条最多尝试三次。

机器异常通知在有渠道订阅时每 60 秒检查一次。面板操作进行中及结束后两分钟内不发送停止通知，同一机器的同一事件十分钟内只发一次。

## 当前状态

| 内容 | 验证情况 |
|---|---|
| 添加账号，创建、启动、停止机器 | 已用真实账号本地验证 |
| 重命名、端口、快照、操作记录筛选 | 只用模拟的 InstaBox 服务测试过 |
| Telegram 和 Webhook 通知 | 没有真实发送过 |
| 机器异常监测 | 只有单元测试 |
| Docker 镜像构建 | CI 已通过 |
| 容器实际运行、Cloudflare Tunnel | 尚未在服务器上验证 |
| macOS 版本 | 只验证了可以编译，没有实际运行过 |

**后续计划：**

- 文件管理
- 远程桌面（noVNC）
- 定时保活
- Telegram 机器人

## 开发

需要 Go 1.26 或更高版本，以及 Node.js 22。

先构建前端，Go 程序会把它嵌入可执行文件：

```bash
cd web
npm ci
npm run build
cd ..
go run ./cmd/instadeck serve
```

运行测试：

```bash
go test ./...
cd web && npm run lint && npm test
```

**技术栈：**

- **后端：** Go、chi、SQLite（`modernc.org/sqlite`，纯 Go，不需要 CGO）
- **前端：** React、Vite、TypeScript、TanStack Query、xterm.js

## 发布新版本

推送 `v` 开头的版本标签即可：

```bash
git tag v1.0.0
git push origin v1.0.0
```

GitHub Actions 会依次完成：

1. 运行全部测试。
2. 发布 Docker 镜像，标签为 `1.0.0` 和 `1.0`。
3. 编译各平台的压缩包，生成 `SHA256SUMS`，创建 GitHub Release 并自动生成更新说明。

带连字符的标签（如 `v1.0.0-rc.1`）会标记为预发布，也不会更新 `1.0` 镜像标签。

下载后可以校验文件：

```bash
sha256sum --ignore-missing -c SHA256SUMS
```

也可以在本地生成同样的压缩包，结果在 `dist/` 目录，需要 Bash、Go、Node.js 和 Python：

```bash
bash scripts/build-release.sh v1.0.0
```

Windows 不记录文件的执行权限，在 Windows 上打出的 Linux 和 macOS 压缩包，解压后要先运行 `chmod +x instadeck`。Release 里的压缩包在 Linux 上构建，没有这个问题。

## 许可证

暂未选择许可证。在选定之前，本仓库的代码不授予他人使用、修改或分发的权利。
