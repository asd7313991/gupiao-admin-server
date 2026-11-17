package mobile

import (
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"api-server/util/customerservice"
	"github.com/gin-gonic/gin"
)

func CustomerService(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var setting system.AppSystemSetting
	if err := pgdb.GetClient().First(&setting).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "客服配置读取失败，请稍后重试")
		return
	}
	link, err := customerservice.FromConfig(setting.Config)
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "客服链接配置异常，请联系平台管理员")
		return
	}
	response.ReturnData(c, gin.H{"url": link})
}
