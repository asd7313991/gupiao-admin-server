package open

import (
	"github.com/gin-gonic/gin"

	"api-server/api/app/v1/open/health"
	"api-server/api/app/v1/open/mobile"
)

// RegisterRoutes 注册所有开放路由
func RegisterRoutes(open *gin.RouterGroup) {
	if open == nil {
		return
	}
	health.RegisterOpenRoutes(open)
	mobile.RegisterRoutes(open)
	open.GET("/news", mobile.ListNews)
	open.GET("/news/categories", mobile.ListNewsCategories)
	open.GET("/news/:id", mobile.GetNewsByID)
	open.GET("/securities/:code/news", mobile.ListSecurityNews)
}
