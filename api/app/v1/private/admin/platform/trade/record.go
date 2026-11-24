package trade

import (
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

type recordResponse struct {
	RealizedProfit *float64 `json:"realized_profit"`
	RealizedCost   *float64 `json:"realized_cost"`
	ID             uint     `json:"id"`
	CustomerID     uint     `json:"customer_id"`
	Symbol         string   `json:"symbol"`
	StockName      string   `json:"stock_name"`
	Currency       string   `json:"currency"`
	Direction      string   `json:"direction"`
	TradePrice     float64  `json:"trade_price"`
	Quantity       float64  `json:"quantity"`
	Amount         float64  `json:"amount"`
	QuantityBasis  uint     `json:"quantity_basis"`
	StampDuty      float64  `json:"stamp_duty"`
	TransferFee    float64  `json:"transfer_fee"`
	Commission     float64  `json:"commission"`
	Remark         string   `json:"remark"`
	TradeAt        int64    `json:"trade_at"`
	CustomerName   string   `json:"customer_name"`
	CustomerPhone  string   `json:"customer_phone"`
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
	input.RealizedProfit, input.RealizedCost = nil, nil
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

func AdjustRecordBasis(c *gin.Context) {
	var input struct {
		ID            uint   `json:"id" form:"id"`
		QuantityBasis uint   `json:"quantity_basis" form:"quantity_basis"`
		Reason        string `json:"reason" form:"reason"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 || input.QuantityBasis > 1 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "成交记录或口径参数无效")
		return
	}
	input.Reason = strings.TrimSpace(input.Reason)
	if input.Reason == "" || utf8.RuneCountInString(input.Reason) > 500 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请填写不超过500字的修正原因")
		return
	}

	var record system.TradeRecord
	var rejection string
	err := pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&record, input.ID).Error; err != nil {
			return err
		}
		symbol := strings.ToUpper(strings.TrimSpace(record.Symbol))
		if !strings.HasSuffix(symbol, ".SH") && !strings.HasSuffix(symbol, ".SZ") && !strings.HasSuffix(symbol, ".BJ") {
			rejection = "口径修正仅支持A股成交记录"
			return fmt.Errorf("%s", rejection)
		}
		if record.QuantityBasis == input.QuantityBasis {
			rejection = "该成交记录已经是所选口径"
			return fmt.Errorf("%s", rejection)
		}
		review := system.TradeRecordBasisReview{
			TradeRecordID: record.ID,
			ReviewerID:    middleware.GetCurrentUserID(c),
			BeforeBasis:   record.QuantityBasis,
			AfterBasis:    input.QuantityBasis,
			Reason:        input.Reason,
		}
		if err := tx.Model(&record).Update("quantity_basis", input.QuantityBasis).Error; err != nil {
			return err
		}
		if err := tx.Create(&review).Error; err != nil {
			return err
		}
		record.QuantityBasis = input.QuantityBasis
		return nil
	})
	if rejection != "" {
		response.ReturnError(c, response.FAILED_PRECONDITION, rejection)
		return
	}
	if err != nil {
		if err == gorm.ErrRecordNotFound {
			response.ReturnError(c, response.NOT_FOUND, "成交记录不存在")
			return
		}
		response.ReturnError(c, response.DATA_LOSS, "修正成交口径失败")
		return
	}
	response.ReturnData(c, record)
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
