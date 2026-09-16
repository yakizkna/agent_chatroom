package handlers

import (
	"crypto/subtle"
	"net/http"
	"os"
	"strings"

	"github.com/gin-gonic/gin"
	"golang.org/x/crypto/bcrypt"
)

// AdminLogin 统一登录：账号密码换 JWT（独立服务自带登录，与 yakisite 共享 AUTH_JWT_SECRET / ADMIN_USER / ADMIN_PASSWORD）。
// POST /api/chat/login { "username": "...", "password": "..." }
//   → 200 { "token": "<jwt>", "expires_at": <unix> }
//   → 401 { "error": "unauthorized" }
//
// 支持两套账号（与 yakisite 的 admin_login.go 对齐）：
//   - 主管理员账号：ADMIN_USER / ADMIN_PASSWORD
//   - agent 管理员账号：AGENT_ADMIN_USER / AGENT_ADMIN_PASSWORD（独立账号，role 仍为 "admin"）
// 密码均为 bcrypt 哈希；签出的 JWT 用共享 AUTH_JWT_SECRET 校验，与 yakisite 互相可验签。
func AdminLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	mainUser := os.Getenv("ADMIN_USER")
	mainHash := os.Getenv("ADMIN_PASSWORD")
	agentUser := os.Getenv("AGENT_ADMIN_USER")
	agentHash := os.Getenv("AGENT_ADMIN_PASSWORD")

	// 两套账号均未配置 → 认证禁用（fail-closed）
	if (mainUser == "" || mainHash == "") && (agentUser == "" || agentHash == "") {
		c.JSON(http.StatusServiceUnavailable, gin.H{"error": "auth disabled"})
		return
	}

	var sub string
	if mainUser != "" && mainHash != "" &&
		subtle.ConstantTimeCompare([]byte(req.Username), []byte(mainUser)) == 1 {
		if bcrypt.CompareHashAndPassword([]byte(mainHash), []byte(req.Password)) == nil {
			sub = mainUser
		}
	} else if agentUser != "" && agentHash != "" &&
		subtle.ConstantTimeCompare([]byte(req.Username), []byte(agentUser)) == 1 {
		if bcrypt.CompareHashAndPassword([]byte(agentHash), []byte(req.Password)) == nil {
			sub = agentUser
		}
	}

	if sub == "" {
		c.JSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
		return
	}

	token, exp, err := SignAdminJWT(sub)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": "internal error"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"token": token, "expires_at": exp})
}

// RequireAdminJWT 管理端 JWT 校验中间件（鉴权，fail-closed）。被 chat.go 的 ChatRooms.Auth() 复用。
func RequireAdminJWT() gin.HandlerFunc {
	return func(c *gin.Context) {
		auth := c.GetHeader("Authorization")
		token, ok := strings.CutPrefix(auth, "Bearer ")
		if !ok || token == "" {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		if _, valid := VerifyAdminJWT(token); !valid {
			c.AbortWithStatusJSON(http.StatusUnauthorized, gin.H{"error": "unauthorized"})
			return
		}
		c.Next()
	}
}