package setting

import (
	"encoding/json"

	"github.com/gin-gonic/gin"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

func GetSystemSetting(c *gin.Context) {
	var setting system.AppSystemSetting
	if err := pgdb.GetClient().First(&setting).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取系统设置失败")
		return
	}
	var config any
	if err := json.Unmarshal([]byte(setting.Config), &config); err != nil {
		response.ReturnError(c, response.DATA_LOSS, "系统设置格式错误")
		return
	}
	response.ReturnData(c, config)
}

func SaveSystemSetting(c *gin.Context) {
	var config any
	if !middleware.CheckParam(&config, c) {
		return
	}
	content, err := json.Marshal(config)
	if err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "系统设置格式错误")
		return
	}
	var setting system.AppSystemSetting
	if err := pgdb.GetClient().First(&setting).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取系统设置失败")
		return
	}
	if err := pgdb.GetClient().Model(&setting).Update("config", string(content)).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "保存系统设置失败")
		return
	}
	response.ReturnData(c, nil)
}
