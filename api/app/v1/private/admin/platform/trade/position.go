package trade

import (
	"fmt"
	"math"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

func ListPositions(c *gin.Context) {
	var items []system.TradePosition
	db := pgdb.GetClient().Where("trade_positions.position_qty > 0").Order("trade_positions.id DESC")
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
	if input.Leverage < 1 {
		input.Leverage = 5
	}
	input.TradePosition.Margin = input.TotalCost
	input.TradePosition.MarketValue = input.PositionQty * input.CurrentPrice * input.Leverage
	input.TradePosition.ProfitLoss = (input.PositionQty*input.CurrentPrice - input.TotalCost) * input.Leverage
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

func ForceClosePosition(c *gin.Context) {
	var input struct {
		ID uint `json:"id"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "持仓 ID 无效")
		return
	}

	var result struct {
		ID           uint    `json:"id"`
		CustomerID   uint    `json:"customer_id"`
		Symbol       string  `json:"symbol"`
		Price        float64 `json:"price"`
		Quantity     float64 `json:"quantity"`
		Amount       float64 `json:"amount"`
		Realized     float64 `json:"realized"`
		BalanceAfter float64 `json:"balance_after"`
	}
	var rejection string
	err := pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
		var snapshot system.TradePosition
		if err := tx.Select("id", "customer_id").First(&snapshot, input.ID).Error; err != nil {
			return err
		}
		var customer system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, snapshot.CustomerID).Error; err != nil {
			return err
		}
		var position system.TradePosition
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&position, input.ID).Error; err != nil {
			return err
		}
		if position.PositionQty <= 0 || position.Status != system.StatusEnabled {
			rejection = "该持仓已关闭，无需重复强平"
			return nil
		}
		var security system.StockSecurity
		if err := tx.Where("symbol = ?", position.Symbol).First(&security).Error; err != nil {
			return err
		}
		if security.LastPrice <= 0 {
			rejection = "证券当前价格无效，无法强平"
			return nil
		}

		price := security.LastPrice
		quantity := position.PositionQty
		amount, realized, settlement := calculateForceCloseSettlement(position, price)
		customer.Balance = roundPositionMoney(customer.Balance + settlement)
		if realized >= 0 {
			customer.TotalProfit = roundPositionMoney(customer.TotalProfit + realized)
		} else {
			customer.TotalLoss = roundPositionMoney(customer.TotalLoss - realized)
		}
		closedAt := time.Now().Unix()
		if err := tx.Model(&system.LimitOrder{}).
			Where("customer_id = ? AND symbol = ? AND direction = ? AND status = ? AND deleted_at IS NULL", customer.ID, position.Symbol, "卖出", "pending").
			Updates(map[string]any{"status": "cancelled", "cancelled_at": closedAt, "frozen_quantity": 0}).Error; err != nil {
			return err
		}
		record := system.TradeRecord{CustomerID: customer.ID, Symbol: position.Symbol, StockName: position.StockName, Currency: position.Currency, Direction: "卖出", TradePrice: price, Quantity: quantity, Amount: amount, Remark: "后台按当前价格强平", TradeAt: closedAt}
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		fundDirection := "入账"
		fundAmount := settlement
		if fundAmount < 0 {
			fundDirection = "出账"
			fundAmount = -fundAmount
		}
		if err := tx.Create(&system.CustomerFundRecord{CustomerID: customer.ID, Type: "后台强平", Direction: fundDirection, Currency: position.Currency, Amount: roundPositionMoney(fundAmount), Balance: customer.Balance, Remark: fmt.Sprintf("%s %s 按当前价 %.4f 强平 %.0f 股", position.StockName, position.Symbol, price, quantity)}).Error; err != nil {
			return err
		}
		position.PositionQty, position.AvailableQty = 0, 0
		position.TotalCost, position.Margin, position.MarketValue = 0, 0, 0
		position.CostPrice, position.ProfitLoss, position.ProfitRate = 0, 0, 0
		position.CurrentPrice, position.Status = price, system.StatusDisabled
		if err := tx.Save(&customer).Error; err != nil {
			return err
		}
		if err := tx.Save(&position).Error; err != nil {
			return err
		}
		result.ID, result.CustomerID, result.Symbol = position.ID, customer.ID, position.Symbol
		result.Price, result.Quantity, result.Amount = price, quantity, amount
		result.Realized, result.BalanceAfter = realized, customer.Balance
		return nil
	})
	if rejection != "" {
		response.ReturnError(c, response.FAILED_PRECONDITION, rejection)
		return
	}
	if err == gorm.ErrRecordNotFound {
		response.ReturnError(c, response.NOT_FOUND, "持仓、客户或证券不存在")
		return
	}
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "强平失败，请稍后重试")
		return
	}
	response.ReturnData(c, result)
}

func calculateForceCloseSettlement(position system.TradePosition, price float64) (amount, realized, settlement float64) {
	leverage := position.Leverage
	if leverage < 1 {
		leverage = 1
	}
	amount = roundPositionMoney(price * position.PositionQty)
	realized = roundPositionMoney((amount - position.TotalCost) * leverage)
	settlement = roundPositionMoney(position.Margin + realized)
	return amount, realized, settlement
}

func roundPositionMoney(value float64) float64 {
	return math.Round(value*100) / 100
}
