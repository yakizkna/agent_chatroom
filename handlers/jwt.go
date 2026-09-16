package handlers

import (
	"os"
	"strconv"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// jwtSecret 读取共享密钥（AUTH_JWT_SECRET）。
func jwtSecret() []byte {
	return []byte(os.Getenv("AUTH_JWT_SECRET"))
}

// jwtTTL 返回 JWT 有效期（秒），默认 8h。
func jwtTTL() time.Duration {
	if v := os.Getenv("AUTH_JWT_TTL"); v != "" {
		if sec, err := strconv.ParseInt(v, 10, 64); err == nil && sec > 0 {
			return time.Duration(sec) * time.Second
		}
	}
	return 8 * time.Hour
}

// SignAdminJWT 用统一账号签发 JWT。
func SignAdminJWT(username string) (string, int64, error) {
	now := time.Now()
	exp := now.Add(jwtTTL())
	claims := jwt.MapClaims{
		"sub":  username,
		"role": "admin",
		"iat":  now.Unix(),
		"exp":  exp.Unix(),
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString(jwtSecret())
	return signed, exp.Unix(), err
}

// VerifyAdminJWT 校验 JWT，返回用户名与是否有效。
func VerifyAdminJWT(tokenStr string) (string, bool) {
	if tokenStr == "" || len(jwtSecret()) == 0 {
		return "", false
	}
	token, err := jwt.Parse(tokenStr, func(t *jwt.Token) (interface{}, error) {
		if _, ok := t.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, jwt.ErrSignatureInvalid
		}
		return jwtSecret(), nil
	})
	if err != nil || !token.Valid {
		return "", false
	}
	claims, ok := token.Claims.(jwt.MapClaims)
	if !ok {
		return "", false
	}
	sub, _ := claims["sub"].(string)
	return sub, sub != ""
}
