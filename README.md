# InstaDeck

InstaDeck 是一个自托管的管理面板，用来在一个页面里统一管理多个 [InstaBox](https://instabox.sh) 账号的云端 Linux 机器，不用来回切换账号。

> **项目处于早期开发阶段。** 第一版已经可以在本地运行，但只用模拟的 InstaBox 服务测试过，尚未用真实账号验证，部署配置也没有实际运行过。

本项目是独立的第三方工具，与 InstaBox 官方无关。使用多个账号前，请自行确认符合 InstaBox 的服务条款。

## 计划功能

第一版计划包含：

- **账号管理：** 添加 API Key，查看每个账号的用量和配额。
- **统一机器列表：** 跨账号显示所有机器，可按账号和状态筛选。单个账号出错不影响其他账号。
- **机器操作：** 启动、停止、暂停、恢复、创建和删除，并显示操作进度。
- **网页终端：** 在面板里直接打开机器的终端。

## 开发进度

- [x] 安全底座：配置、管理员登录和两步验证、会话、密钥加密、审计事件模型
- [x] SQLite 存储和 InstaBox API 客户端
- [x] 账号管理和跨账号机器聚合
- [x] 机器操作和网页终端
- [x] HTTP 接口、管理命令和部署配置（部署配置尚未实际验证）
- [x] 前端界面（机器、终端、账号、审计、安全设置、外观主题）
- [x] 零配置启动和网页首次设置向导
- [ ] 用真实 InstaBox 账号验证
- [ ] 后续：通知渠道、端口转发、快照、文件管理、远程桌面、定时保活

## 技术栈

- **后端：** Go、chi、SQLite（`modernc.org/sqlite`）
- **前端：** React、Vite、TypeScript、Tailwind CSS、shadcn/ui、TanStack Query、TanStack Table、xterm.js
- **部署：** 单一可执行文件，由反向代理（计划使用 Caddy）提供 HTTPS

## 安全设计

这个面板集中保存了多个账号的 API Key，一旦被攻破，所有已接入的账号都会受影响。因此设计上：

- 管理员登录必须通过密码和 TOTP 两步验证。
- 应用只监听回环地址，由同一台服务器上的反向代理提供 HTTPS。
- API Key 用 AES-256-GCM 加密存储，主密钥放在独立文件中，Key 不会发送到浏览器。

本地加密只能防止数据库文件泄露，无法防止服务器本身被攻破。建议只给 API Key 必需的权限，并在怀疑泄露时立即到 InstaBox 吊销。

## 快速开始

构建前端和程序后，直接运行：

```bash
instadeck serve
```

打开 http://127.0.0.1:8080，按向导创建管理员、扫描二维码绑定两步验证，并保存恢复码。无需预先配置环境变量或用命令行生成主密钥。

服务器部署使用 `deploy/instadeck.service` 和 `deploy/Caddyfile`：安装程序并创建服务用户后，只需修改 Caddyfile 的域名，由 Caddy 提供 HTTPS。首次设置码在服务日志中查看：`journalctl -u instadeck`，用于防止他人抢先设置。

默认数据库 `panel.db` 和主密钥 `master.key` 都在当前目录的 `instadeck-data` 中；systemd 部署使用 `/var/lib/instadeck`。请备份数据库，并单独保管主密钥；主密钥丢失后无法解密 API Key。`admin backup` 仅备份数据库。全部环境变量都是可选覆盖，参见 `deploy/.env.example`。

更换域名可运行 `instadeck admin set-origin https://新域名`，然后重启服务；环境变量 `INSTADECK_ORIGIN` 优先于保存的网址。管理命令须使用与服务相同的数据目录。`admin init` 保留作备用；只有验证器和恢复码都丢失时，使用 `admin reset-auth` 重置认证，随后重启服务。

## 开发

需要 Go 1.26 或更高版本。

```bash
go test ./internal/...
```

## 许可证

暂未选择许可证。
