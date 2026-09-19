package main

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"agent_chatroom/config"
	"agent_chatroom/handlers"
)

func main() {
	// 命令行模式（不进服务）：`agent_chatroom archive [--dry] <房间目录>…`
	// 用途：把「批量归档」做成可被 ra_tasks / 运维台定期或手动调用的动作 ——
	// 解决「纯 git 直写房间不会触发归档」的问题，也让归档彻底离开发言热路径。
	if len(os.Args) > 1 && os.Args[1] == "archive" {
		os.Exit(runArchive(os.Args[2:]))
	}

	// 配置：**唯一来源 = YAML**（config.yaml）。不再读任何 CHATROOM_* / AUTH_* 环境变量。
	cfg, err := config.Load(configPath())
	if err != nil {
		fmt.Fprintf(os.Stderr, "[启动失败] %v\n", err)
		os.Exit(1)
	}
	config.Set(cfg)
	fmt.Printf("配置：%s（端口 %d，聊天室 %d 个）\n", cfg.Path(), cfg.Port, len(cfg.ResolvedRooms()))
	for _, r := range cfg.ResolvedRooms() {
		mark := "CHAT.md ✓"
		if _, e := os.Stat(filepath.Join(r.Path, "CHAT.md")); e != nil {
			mark = "缺 CHAT.md ✗"
		}
		fmt.Printf("  · %-16s id=%-20s 鉴权=%-5v 输入表单=%-5v %s\n",
			r.Name, r.ID, r.IsAuth, r.ShowInputForm, mark)
	}

	r := gin.Default()

	// 根路径重定向到沟通室页面（直接访问 :8093/ 也能打开，而非 404）
	r.GET("/", func(c *gin.Context) {
		c.Redirect(302, "/chatroom")
	})

	// 沟通室页面（静态 HTML + 注入「自动刷新间隔」配置）
	// 页面是纯静态文件，但「自动刷新间隔」来自配置 ui.auto_refresh_sec
	// ⇒ 服务端在 `</head>` 前插一段 <script>window.__AUTO_REFRESH_SEC__ = <n>;</script>，
	// 页面读取它（未注入时用页面默认值）。这样改配置无需改前端代码、也不额外发请求。
	r.GET("/chatroom", func(c *gin.Context) {
		html, err := os.ReadFile("./static/pages/chatroom.html")
		if err != nil {
			c.String(http.StatusInternalServerError, "读取沟通室页面失败：%v", err)
			return
		}
		inject := fmt.Sprintf("<script>window.__AUTO_REFRESH_SEC__ = %d;</script>\n</head>", chatAutoRefreshSec())
		c.Data(http.StatusOK, "text/html; charset=utf-8",
			[]byte(strings.Replace(string(html), "</head>", inject, 1)))
	})

	chat := handlers.NewChatRooms(cfg.ResolvedRooms())
	// /api/chat/* 鉴权：房间 is_auth=false 免登录，其余走管理端 JWT。
	// 登录接口独立挂载（不经 JWT）：/api/chat/login 把账号密码转发到鉴权服务换 JWT，本地验签。
	r.POST("/api/chat/login", handlers.AdminLogin)
	chatGroup := r.Group("/api/chat", chat.Auth())
	{
		chatGroup.GET("", chat.GetChat)
		chatGroup.POST("/speak", chat.SpeakHTTP)
		chatGroup.POST("/update", chat.Update)
		chatGroup.GET("/file/*filepath", chat.ChatFile) // 仓库内文件代理（替代 GitHub raw，支持私有仓库）
	}

	r.Run(":" + strconv.Itoa(cfg.Port))
}

// configPath 解析配置文件路径：命令行 `-c <路径>` / `--config <路径>` > 环境变量 CHATROOM_CONFIG > 缺省 config.yaml。
func configPath() string {
	args := os.Args[1:]
	for i, a := range args {
		switch {
		case a == "-c" || a == "--config":
			if i+1 < len(args) {
				return args[i+1]
			}
		case strings.HasPrefix(a, "-c="):
			return strings.TrimPrefix(a, "-c=")
		case strings.HasPrefix(a, "--config="):
			return strings.TrimPrefix(a, "--config=")
		}
	}
	return os.Getenv("CHATROOM_CONFIG")
}

// runArchive 实现 `agent_chatroom archive [--dry] <房间目录>…`：
// 逐房间检查 `CHAT.md` 是否超过归档上限（200 条），超过则**批量归档**（保留最近 100 条）并
// `pull --rebase` → `commit` → `push`。返回进程退出码（0 = 全部成功）。
//
// 给 ra_tasks（`room_archive/archive.py`）或运维台调用；`--dry` 只报告不落盘。
func runArchive(args []string) int {
	dry, dirs := false, []string{}
	for _, a := range args {
		switch a {
		case "--dry", "-n":
			dry = true
		case "-h", "--help":
			fmt.Println("用法: agent_chatroom archive [--dry] <房间目录>…")
			fmt.Println("  检查各房间 CHAT.md，超过 200 条则批量归档最旧的 ~100 条（保留最近 100 条）并提交推送。")
			fmt.Println("  --dry 只报告将要做的事，不写文件、不动 git。")
			return 0
		default:
			if strings.HasPrefix(a, "-") {
				fmt.Fprintf(os.Stderr, "未知参数：%s（-h 看用法）\n", a)
				return 2
			}
			dirs = append(dirs, a)
		}
	}
	if len(dirs) == 0 {
		fmt.Fprintln(os.Stderr, "用法: agent_chatroom archive [--dry] <房间目录>…")
		return 2
	}
	fail := 0
	for _, d := range dirs {
		abs, err := filepath.Abs(d)
		if err != nil {
			fmt.Printf("✗ %v\n", err)
			fail++
			continue
		}
		line, err := handlers.NewChatHandler(abs).ArchiveIfNeeded(dry)
		if err != nil {
			fmt.Printf("✗ %v\n", err)
			fail++
			continue
		}
		fmt.Printf("✓ %s\n", line)
	}
	if fail > 0 {
		return 1
	}
	return 0
}

// chatAutoRefreshSec 读取沟通室页面的「自动刷新间隔」（秒）。口径（与页面一致）：
//
//	配置 ui.auto_refresh_sec 省略 → 30（默认）
//	= 0 或负数                    → 0 = **不自动刷新**（页面隐藏该勾选框）
//	= 1..4                        → 页面按下限 5s 处理（防误配成狂刷）
func chatAutoRefreshSec() int {
	cfg := config.Get()
	if cfg == nil || cfg.UI.AutoRefreshSec == nil {
		return 30
	}
	if *cfg.UI.AutoRefreshSec < 0 {
		return 0
	}
	return *cfg.UI.AutoRefreshSec
}
