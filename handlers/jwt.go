package handlers

import (
	"os"

	"github.com/golang-jwt/jwt/v5"
)

// jwtSecret 读取共享密钥（AUTH_JWT_SECRET）。
func jwtSecret() []byte {
	return []byte(os.Getenv("AUTH_JWT_SECRET"))
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
