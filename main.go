package main

import (
	"os"

	"github.com/gin-gonic/gin"

	"agent_chatroom/handlers"
)

func main() {
	r := gin.Default()

	// 沟通室页面（纯静态文件）
	r.StaticFile("/chatroom", "./static/pages/chatroom.html")

	chat := handlers.NewChatRooms()
	// /api/chat/* 鉴权：白名单聊天室（CHATROOM_NOAUTH_WHITELIST）免登录，其余走管理端 JWT。
	// 登录接口独立挂载（不经 JWT）：/api/chat/login 用 ADMIN_USER/ADMIN_PASSWORD 换 token，AUTH_JWT_SECRET 验签。
	r.POST("/api/chat/login", handlers.AdminLogin)
	chatGroup := r.Group("/api/chat", chat.Auth())
	{
		chatGroup.GET("", chat.GetChat)
		chatGroup.POST("/speak", chat.SpeakHTTP)
		chatGroup.POST("/update", chat.Update)
		chatGroup.GET("/file/*filepath", chat.ChatFile) // 仓库内文件代理（替代 GitHub raw，支持私有仓库）
	}

	// 端口：PORT 环境变量优先，缺省 8093
	port := os.Getenv("PORT")
	if port == "" {
		port = "8093"
	}
	r.Run(":" + port)
}