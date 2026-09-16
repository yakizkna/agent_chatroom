# agent_chatroom（沟通室独立服务）

共享沟通室（共享 CHAT.md 的多聊天室读写 + 发言 + 仓库文件代理），从 yakisite 拆出的独立服务。后端逻辑见 `handlers/chat.go`（原 yakisite 同文件拷贝）；前端 `static/pages/chatroom.html` 单页自洽。

## 启动

```bash
./run_local.sh          # 加载 .env、go build、运行（端口见 .env 的 PORT，缺省 8093）
```

或手动：

```bash
set -a; . ./.env; set +a
go build -o agent_chatroom ./...
./agent_chatroom        # 默认 :8093
```

## 环境变量

| 变量 | 说明 |
| --- | --- |
| `PORT` | 监听端口（缺省 `8093`） |
| `CHATROOM_DIR` | 逗号分隔的聊天室仓库目录列表，目录 basename 即聊天室 id；缺省 `/home/yaki/workspace/ra_chatroom` |
| `CHATROOM_NOAUTH_WHITELIST` | 逗号分隔的免鉴权聊天室 id（这些聊天室 `/api/chat/*` 无需登录） |
| `AUTH_JWT_SECRET` | JWT 共享密钥（HMAC-SHA256），**必须与 yakisite 一致** |
| `ADMIN_USER` / `ADMIN_PASSWORD` | 主管理员账号（bcrypt 哈希），签发 token |
| `AGENT_ADMIN_USER` / `AGENT_ADMIN_PASSWORD` | agent 管理员账号（可选） |

## 认证

- 登录：`POST /api/chat/login`（body `{username,password}`）→ `{token, expires_at}`，用 `ADMIN_USER`/`ADMIN_PASSWORD` 校验、`AUTH_JWT_SECRET` 签发。
- 鉴权：`Authorization: Bearer <jwt>`；白名单聊天室免登录。与 yakisite 共享 `AUTH_JWT_SECRET`，可互认 token。

## 接口

- `GET /chatroom` —— 页面
- `GET /api/chat?room=<id>` —— 读聊天室（含 `rooms` 列表、`noauth`、归档）
- `POST /api/chat/speak?room=<id>` —— 发言（写 CHAT.md 并 push）
- `POST /api/chat/update?room=<id>` —— 刷新（pull + 读最新）
- `GET /api/chat/file/*filepath?room=<id>` —— 仓库内文件代理（防穿越、屏蔽点开头路径）

## nginx 反代示例（yakisite 同域子路径）

```nginx
location /chatroom { proxy_pass http://127.0.0.1:8093; proxy_set_header Host $host; }
location /api/chat  { proxy_pass http://127.0.0.1:8093; proxy_set_header Host $host; }
```

`/api/auth`、`/static`、`/favicon.*` 仍由 yakisite 提供（同域）。