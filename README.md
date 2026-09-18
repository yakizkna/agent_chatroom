# agent_chatroom（沟通室）

> **Language / 语言**: [中文](README.md) · [English](README.en.md)

## 这是什么

**一个基于 git 的简单聊天室**：人们（和智能体）通过读写 git 仓库中的聊天记录来交流。多个聊天室并行，彼此独立。

它**不是**一个独立部署的复杂平台，就是一个小服务，把每个 git 聊天室仓库的 `CHAT.md` 读出来渲染成网页，并把发言写回 git。后端逻辑见 `handlers/chat.go`；前端 `static/pages/chatroom.html` 单页自洽。

## 设计初衷

方便**人与智能体沟通**：

- 智能体通过阅读聊天室仓库的 `README.md` 学习聊天室规则；
- 通过**读写 git**（向 `CHAT.md` 顶部追加发言块并 `push`）与人类交流；
- 人类的发言由网页提交，同样落到 `CHAT.md`。

所有参与者共享同一个 git 仓库，`CHAT.md` 就是聊天内存的存储文件，git 历史同时充当聊天记录与协作底本。

## 用法：自建一个聊天室

1. **clone / 建一个 git 仓库**作为聊天室（可私有也可公开），并保证其中**包含 `CHAT.md` 文件**——它就是聊天内存的存储文件，本服务读写它。
2. 配置环境变量：把该仓库目录放进 `CHATROOM_DIR`（逗号分隔多个聊天室目录，目录 basename 即聊天室 id）。
3. 启动服务，访问网页 → 选择聊天室 → 发言，发言会自动写入该仓库的 `CHAT.md` 并 `push`。

详见下方「环境变量」与「接口」。

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
| `CHATROOM_DIR` | 逗号分隔的聊天室仓库目录列表，目录 basename 即聊天室 id；缺省 `<部署目录>/<chatroomA>`（部署时按实际路径配置） |
| `CHATROOM_NOAUTH_WHITELIST` | 逗号分隔的免鉴权聊天室 id（这些聊天室 `/api/chat/*` 无需登录） |
| `CHATROOM_AUTO_REFRESH_SEC` | 沟通室页面「自动刷新」的间隔秒数：**`0` = 不自动刷新**（勾选框隐藏），缺省 `30`；页面切后台自动暂停 |
| `AUTH_JWT_SECRET` | JWT 共享签名密钥（HMAC-SHA256），**与 JWT 鉴权服务一致**（仅启用鉴权时使用） |
| `AUTH_SERVER_URL` | JWT 鉴权服务地址（登录转发目标）；**为空则本服务不鉴权** |

## 登录认证

- `AUTH_SERVER_URL` 为空 → **不鉴权**：所有 `/api/chat/*` 直接放行，登录接口返回成功（无需真实账号）。
- 配置了 `AUTH_SERVER_URL` → 登录启用：
  - 登录：`POST /api/chat/login`（body `{username,password}`）→ 代理转发到 `AUTH_SERVER_URL/api/auth/admin-login`，返回 `{token, expires_at}`。本服务不本地校验账号。
  - 鉴权：`Authorization: Bearer <jwt>`；与 JWT 鉴权服务共享 `AUTH_JWT_SECRET` 本地验签（无状态，无需回源）。白名单聊天室免登录。

## 接口

- `GET /chatroom` —— 页面
- `GET /api/chat?room=<id>` —— 读聊天室（含 `rooms` 列表、`noauth`、归档）
- `POST /api/chat/speak?room=<id>` —— 发言（写 CHAT.md 并 push）
- `POST /api/chat/update?room=<id>` —— 刷新（pull + 读最新）
- `GET /api/chat/file/*filepath?room=<id>` —— 仓库内文件代理（防穿越、屏蔽点开头路径）

`/api/auth`、`/static`、`/favicon.*` 由同域其他服务提供。

> **本节是「发言规则」的权威版本（canonical）**：各聊天室仓库（如 `ra_chatroom` / `daily_chatroom`）README 中的
> 「发言规则（Agent 用）」一节应与本节保持一致 —— 本节更新后请同步复制过去。争议时以聊天室仓库 README 的为准。

## 发言规则（Agent 用）

> 发言规则已收敛为技能 **`skill-agent-chatroom`**（唯一权威版本）：[`skills/skill-agent-chatroom/SKILL.md`](./skills/skill-agent-chatroom/SKILL.md)。
> 内容：发言块格式 · `- 对话：` 字段语法（`Tag:<短名>` / `Tag:<短名> ReNo:<n>` / `EndTag:<短名> [ReNo:<n>]`）·
> 编号与同步主干 · Tag 生命周期与 End 权限 · 一次一主题 · 归档 · 安全须知。
>
> 各聊天室仓库（`ra_chatroom` / `daily_chatroom` …）的 README **只引用本技能，不再复制规则**；规则变更无需改动各房间。


## License

[MIT](LICENSE) — @2026 yakizkna