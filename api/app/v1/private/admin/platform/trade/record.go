package trade

import (
	"time"

	"github.com/gin-gonic/gin"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

type recordResponse struct {
	system.TradeRecord
	CustomerName  string `json:"customer_name"`
	CustomerPhone string `json:"customer_phone"`
}

func ListRecords(c *gin.Context) {
	page, pageSize := middleware.GetPage(c), middleware.GetPageSize(c)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	db := pgdb.GetClient().Table("trade_records").Select("trade_records.*, customers.name AS customer_name, customers.phone AS customer_phone").Joins("LEFT JOIN customers ON customers.id = trade_records.customer_id").Where("trade_records.deleted_at IS NULL")
	if phone := c.Query("phone"); phone != "" {
		db = db.Where("customers.phone LIKE ?", "%"+phone+"%")
	}
	if symbol := c.Query("symbol"); symbol != "" {
		db = db.Where("trade_records.symbol LIKE ?", "%"+symbol+"%")
	}
	if direction := c.Query("direction"); direction != "" {
		db = db.Where("trade_records.direction = ?", direction)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询交易记录失败")
		return
	}
	var items []recordResponse
	if err := db.Order("trade_records.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Scan(&items).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询交易记录失败")
		return
	}
	response.ReturnData(c, gin.H{"records": items, "total": total, "page": page, "page_size": pageSize})
}

func SaveRecord(c *gin.Context) {
	var input system.TradeRecord
	if !middleware.CheckParam(&input, c) || input.CustomerID == 0 || input.Symbol == "" || (input.Direction != "买入" && input.Direction != "卖出") {
		response.ReturnError(c, response.INVALID_ARGUMENT, "客户、证券代码和交易方向为必填项")
		return
	}
	if input.TradeAt == 0 {
		input.TradeAt = time.Now().Unix()
	}
	input.Amount = input.TradePrice * input.Quantity
	if input.ID == 0 {
		if err := pgdb.GetClient().Create(&input).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "新增交易记录失败")
			return
		}
	} else if err := pgdb.GetClient().Save(&input).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "修改交易记录失败")
		return
	}
	response.ReturnData(c, input)
}

func DeleteRecord(c *gin.Context) {
	var input struct {
		ID uint `json:"id"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "交易记录 ID 无效")
		return
	}
	if err := pgdb.GetClient().Delete(&system.TradeRecord{}, input.ID).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "删除交易记录失败")
		return
	}
	response.ReturnData(c, nil)
}
