package handlers

import (
	"github.com/golang-jwt/jwt/v5"

	"agent_chatroom/config"
)

// jwtSecret 读取共享密钥（配置 auth.jwt_secret）。
func jwtSecret() []byte {
	if c := config.Get(); c != nil {
		return []byte(c.Auth.JWTSecret)
	}
	return nil
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
