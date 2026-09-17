package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// authServerURL 返回统一认证服务地址（登录转发目标）。缺省 yakisite 公网地址。
func authServerURL() string {
	if v := os.Getenv("AUTH_SERVER_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "https://yakidev.top"
}

// authClient 发起「服务端到服务端」的登录转发请求。
// 默认禁用系统代理直连（同 ra_duel_bot/auth.py：本机代理挂了/不转发时登录会超时；
// AUTH_USE_PROXY=1 可显式改回走系统代理）。
// 仅用短超时（连接 5s + 整体 10s），避免 yakisite 不可用时前端无响应卡死。
var authClient = &http.Client{
	Transport: newAuthTransport(),
	Timeout:   10 * time.Second,
}

func newAuthTransport() http.RoundTripper {
	tr := &http.Transport{
		DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
	}
	if os.Getenv("AUTH_USE_PROXY") != "1" {
		// 直连：禁用系统 HTTP/SOCKS 代理（同 ra_duel_bot/auth.py）。
		tr.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
	}
	return tr
}

// AdminLogin 统一登录代理：把账号密码转发到统一认证服务（yakisite）换 JWT，
// 本服务不再本地校验账号（凭据只在 yakisite 一份）。与 yakisite 共享 AUTH_JWT_SECRET，
// 拿到的 token 由中间件 RequireAdminJWT 本地验签即可放行。
// POST /api/chat/login { "username": "...", "password": "..." }
//
//	→ 200 { "token": "<jwt>", "expires_at": <unix> }
//	→ 401 { "error": "unauthorized" }
//	→ 502 { "error": "auth service unavailable" }（认证服务不可达）
func AdminLogin(c *gin.Context) {
	var req struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := c.ShouldBindJSON(&req); err != nil || req.Username == "" || req.Password == "" {
		c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request"})
		return
	}

	body, _ := json.Marshal(map[string]string{"username": req.Username, "password": req.Password})
	upstream, err := http.NewRequest(http.MethodPost, authServerURL()+"/api/auth/admin-login",
		bytes.NewReader(body))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "bad auth url"})
		return
	}
	upstream.Header.Set("Content-Type", "application/json")

	resp, err := authClient.Do(upstream)
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "auth service unavailable"})
		return
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 512<<10))
	if err != nil {
		c.JSON(http.StatusBadGateway, gin.H{"error": "auth service unavailable"})
		return
	}
	// 透传统一认证服务的响应（200 带 token / expires_at，401 带 error）。
	c.Data(resp.StatusCode, "application/json; charset=utf-8", raw)
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
