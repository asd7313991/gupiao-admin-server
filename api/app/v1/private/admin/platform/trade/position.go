package trade

import (
	"time"

	"github.com/gin-gonic/gin"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

func ListPositions(c *gin.Context) {
	var items []system.TradePosition
	db := pgdb.GetClient().Order("id DESC")
	if phone := c.Query("phone"); phone != "" {
		db = db.Joins("JOIN customers ON customers.id = trade_positions.customer_id").Where("customers.phone LIKE ?", "%"+phone+"%")
	}
	if symbol := c.Query("symbol"); symbol != "" {
		db = db.Where("symbol LIKE ?", "%"+symbol+"%")
	}
	if name := c.Query("stock_name"); name != "" {
		db = db.Where("stock_name LIKE ?", "%"+name+"%")
	}
	if status := c.Query("status"); status != "" {
		db = db.Where("status = ?", status)
	}
	if err := db.Find(&items).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询持仓失败")
		return
	}
	response.ReturnData(c, items)
}

func SavePosition(c *gin.Context) {
	var input struct {
		system.TradePosition
		RecordChange bool `json:"record_change"`
	}
	if !middleware.CheckParam(&input, c) || input.CustomerID == 0 || input.Symbol == "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "客户和证券代码为必填项")
		return
	}
	if input.BuyAt == 0 {
		input.BuyAt = time.Now().Unix()
	}
	input.TradePosition.TotalCost = input.PositionQty * input.CostPrice
	input.TradePosition.MarketValue = input.PositionQty * input.CurrentPrice
	input.TradePosition.ProfitLoss = input.MarketValue - input.TotalCost
	if input.TradePosition.TotalCost != 0 {
		input.TradePosition.ProfitRate = input.ProfitLoss / input.TotalCost * 100
	}
	if input.Status == 0 {
		input.Status = system.StatusEnabled
	}
	if input.ID == 0 {
		if err := pgdb.GetClient().Create(&input.TradePosition).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "新增持仓失败")
			return
		}
	} else {
		var existing system.TradePosition
		if err := pgdb.GetClient().First(&existing, input.ID).Error; err != nil {
			response.ReturnError(c, response.NOT_FOUND, "持仓不存在")
			return
		}
		input.TradePosition.CreatedAt = existing.CreatedAt
		if err := pgdb.GetClient().Save(&input.TradePosition).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "修改持仓失败")
			return
		}
	}
	response.ReturnData(c, input.TradePosition)
}

func DeletePosition(c *gin.Context) {
	var input struct {
		ID uint `json:"id"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "持仓 ID 无效")
		return
	}
	if err := pgdb.GetClient().Delete(&system.TradePosition{}, input.ID).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "删除持仓失败")
		return
	}
	response.ReturnData(c, nil)
}
