package setting

import (
	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"api-server/util/customerservice"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func GetCustomerService(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var setting system.AppSystemSetting
	if err := pgdb.GetClient().First(&setting).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取客服设置失败")
		return
	}
	link, err := customerservice.FromConfig(setting.Config)
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, err.Error())
		return
	}
	response.ReturnData(c, gin.H{"url": link})
}

func SaveCustomerService(c *gin.Context) {
	var input struct {
		URL *string `json:"url"`
	}
	if !middleware.CheckParam(&input, c) {
		return
	}
	if input.URL == nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请填写客服链接，留空可暂停入口")
		return
	}
	link, err := customerservice.ValidateURL(*input.URL)
	if err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, err.Error())
		return
	}
	err = pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
		var setting system.AppSystemSetting
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&setting).Error; err != nil {
			return err
		}
		content, err := customerservice.WithURL(setting.Config, link)
		if err != nil {
			return err
		}
		return tx.Model(&setting).Update("config", content).Error
	})
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "保存客服链接失败")
		return
	}
	response.ReturnData(c, gin.H{"url": link})
}
