package handlers

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"agent_chatroom/config"
)

// authServerURL 返回 JWT 鉴权服务地址（登录转发目标，配置 auth.server_url）。
// 为空 = 鉴权关闭（不转发、不校验）。
func authServerURL() string {
	if c := config.Get(); c != nil {
		return strings.TrimRight(c.Auth.ServerURL, "/")
	}
	return ""
}

// authEnabled 鉴权是否开启：配置了 auth.server_url 才启用 JWT 鉴权；为空则本服务不鉴权。
func authEnabled() bool {
	return authServerURL() != ""
}

// authClient 发起「服务端到服务端」的登录转发请求。
// 默认禁用系统代理直连（真机部署时本机代理挂掉/不转发，转发会超时；配置 auth.use_proxy=true 改回走系统代理）。
// 仅用短超时（连接 5s + 整体 10s），避免认证服务不可用时前端无响应卡死。
//
// ⚠️ 必须**懒加载**：包级变量在 config.Set() 之前初始化，那时还读不到 use_proxy，
// 若在此处直接构造会把「启用代理」误判为「直连」。
var (
	authClientOnce sync.Once
	authClientVar  *http.Client
)

func authClient() *http.Client {
	authClientOnce.Do(func() {
		tr := &http.Transport{
			DialContext: (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
		}
		useProxy := false
		if c := config.Get(); c != nil {
			useProxy = c.Auth.UseProxy
		}
		if !useProxy {
			// 直连：禁用系统 HTTP/SOCKS 代理。
			tr.Proxy = func(*http.Request) (*url.URL, error) { return nil, nil }
		}
		authClientVar = &http.Client{Transport: tr, Timeout: 10 * time.Second}
	})
	return authClientVar
}

// AdminLogin 统一登录：
//   - 未启用鉴权（auth.server_url 为空）→ 直接返回成功 + 空 token（配合 RequireAdminJWT 放行，即「不鉴权」模式）。
//   - 启用鉴权 → 把账号密码转发到 JWT 鉴权服务的 /api/auth/admin-login 换 JWT，本服务不本地校验账号
//     （凭据只在认证服务一份），拿到的 token 由 RequireAdminJWT 本地验签放行。
//
// POST /api/chat/login { "username": "...", "password": "..." }
//
//	→ 200 { "token": "<jwt>", "expires_at": <unix> }（鉴权关闭时为空 token）
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

	if !authEnabled() {
		// 鉴权关闭：不校验账号，直接返回成功（前端无需真实登录）。
		c.JSON(http.StatusOK, gin.H{"token": "", "expires_at": 0})
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

	resp, err := authClient().Do(upstream)
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
	// 透传 JWT 鉴权服务的响应（200 带 token / expires_at，401 带 error）。
	c.Data(resp.StatusCode, "application/json; charset=utf-8", raw)
}

// RequireAdminJWT 管理端 JWT 校验中间件（鉴权）。被 chat.go 的 ChatRooms.Auth() 复用。
// 未启用鉴权（auth.server_url 为空）→ 直接放行（不鉴权模式）；启用则校验 Bearer JWT（fail-closed）。
func RequireAdminJWT() gin.HandlerFunc {
	return func(c *gin.Context) {
		if !authEnabled() {
			c.Next() // 鉴权关闭：全放行
			return
		}
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
