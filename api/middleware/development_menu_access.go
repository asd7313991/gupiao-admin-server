package middleware

import (
	"github.com/gin-gonic/gin"

	"api-server/config"
)

const localMenuAccessHeader = "X-Local-Menu-Access"
const localMenuAccessValue = "gupiao-menu-dev"

// PlatformMenuAccess 仅允许 Vite 开发代理为本地菜单管理注入管理员上下文。
// 非开发模式始终执行标准 JWT 与超级管理员校验。
func PlatformMenuAccess(c *gin.Context) {
	if config.RunModel == config.RunModelDevValue && c.GetHeader(localMenuAccessHeader) == localMenuAccessValue {
		c.Set("tenant_id", uint(1))
		c.Set("user_id", uint(1))
		c.Set("account", "admin")
		c.Next()
		return
	}

	if !SetTokenClaims(c) || !VerifySuperAdmin(c) {
		return
	}
	c.Next()
}
