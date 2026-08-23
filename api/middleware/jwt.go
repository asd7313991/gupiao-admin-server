package middleware

import (
	"strings"

	"github.com/gin-gonic/gin"

	"api-server/api/auth"
	"api-server/api/response"
)

// SetTokenClaims 校验 JWT 并将用户上下文写入请求。
func SetTokenClaims(c *gin.Context) bool {
	c.FormFile("file") // 防止文件未发送完成就返回错误, 导致前端504而不是正确响应

	authHeader := c.GetHeader("Authorization")
	if authHeader == "" {
		response.ReturnError(c, response.UNAUTHENTICATED, "未携带 token")
		c.Abort()
		return false
	}

	var tokenString string
	// 支持两种格式: "Bearer token" 或直接 "token"
	if strings.HasPrefix(authHeader, "Bearer ") {
		tokenString = strings.TrimPrefix(authHeader, "Bearer ")
	} else {
		tokenString = authHeader
	}

	// 解析多租户JWT
	claims, err := auth.JWTDecrypt(tokenString)
	if err != nil {
		response.ReturnError(c, response.UNAUTHENTICATED, "token 解析失败")
		c.Abort()
		return false
	}

	// 将租户和用户信息存入上下文
	c.Set("tenant_id", claims.TenantID)
	c.Set("user_id", claims.UserID)
	c.Set("account", claims.Account)
	return true
}

// TokenVerify 多租户JWT认证中间件
func TokenVerify(c *gin.Context) {
	if !SetTokenClaims(c) {
		return
	}
	c.Next()
}

// GetTenantID 从上下文获取租户ID
func GetTenantID(c *gin.Context) uint {
	tenantID, exists := c.Get("tenant_id")
	if !exists {
		return 0
	}
	return tenantID.(uint)
}

// GetCurrentUserID 从上下文获取当前用户ID
func GetCurrentUserID(c *gin.Context) uint {
	userID, exists := c.Get("user_id")
	if !exists {
		return 0
	}
	return userID.(uint)
}

// GetCurrentAccount 从上下文获取当前账号
func GetCurrentAccount(c *gin.Context) string {
	account, exists := c.Get("account")
	if !exists {
		return ""
	}
	return account.(string)
}
