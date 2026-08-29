package mobile

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

const (
	limitOrderPending   = "pending"
	limitOrderFilled    = "filled"
	limitOrderCancelled = "cancelled"
)

type limitOrderInput struct {
	Code       string  `json:"code"`
	Direction  string  `json:"direction"`
	LimitPrice float64 `json:"limit_price"`
	Quantity   float64 `json:"quantity"`
}

type limitOrderView struct {
	ID            uint    `json:"id"`
	Code          string  `json:"code"`
	Symbol        string  `json:"symbol"`
	StockName     string  `json:"stock_name"`
	Direction     string  `json:"direction"`
	LimitPrice    float64 `json:"limit_price"`
	Quantity      float64 `json:"quantity"`
	Status        string  `json:"status"`
	StatusLabel   string  `json:"status_label"`
	FrozenAmount  float64 `json:"frozen_amount"`
	ExecutedPrice float64 `json:"executed_price"`
	CreatedAt     int64   `json:"created_at"`
	FilledAt      int64   `json:"filled_at"`
	CancelledAt   int64   `json:"cancelled_at"`
}

func PlaceLimitOrder(c *gin.Context) {
	var input limitOrderInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "委托参数无效")
		return
	}
	input.Code = strings.TrimSpace(input.Code)
	if input.Direction != "买入" && input.Direction != "卖出" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "交易方向无效")
		return
	}
	if input.LimitPrice <= 0 || input.LimitPrice != roundMoney(input.LimitPrice) || input.Quantity <= 0 || input.Quantity != math.Trunc(input.Quantity) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "委托价格最多两位小数，数量必须为正整数")
		return
	}
	settings, err := loadMobileTradeSettings()
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取交易规则失败")
		return
	}
	security, ok := findSecurity(input.Code)
	if !ok || security.Status != system.StatusEnabled || security.LastPrice <= 0 {
		response.ReturnError(c, response.NOT_FOUND, "证券不可交易")
		return
	}
	tradeInput := orderInput{Code: input.Code, Direction: input.Direction, Quantity: input.Quantity}
	if message := validateSecurityTrade(security, tradeInput, settings); message != "" {
		response.ReturnError(c, response.FAILED_PRECONDITION, message)
		return
	}

	var order system.LimitOrder
	var rejection string
	err = pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
		var customer system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, middleware.GetCurrentCustomerID(c)).Error; err != nil {
			return err
		}
		if customer.Status != system.StatusEnabled || customer.FundStatus != system.StatusEnabled || customer.Verified != system.StatusEnabled {
			rejection = "账户未完成认证或当前不可交易"
			return errTradeRejected
		}
		order = system.LimitOrder{CustomerID: customer.ID, Symbol: security.Symbol, StockName: security.Name, Direction: input.Direction, LimitPrice: input.LimitPrice, Quantity: input.Quantity, Status: limitOrderPending}
		if input.Direction == "买入" {
			amount, _, _, _, _, fee := calculateTradeFees(input.LimitPrice, input.Quantity, input.Direction, settings)
			order.FrozenAmount = amount + fee
			if customer.Balance < order.FrozenAmount {
				rejection = fmt.Sprintf("可用余额不足，还需 %.2f 元", order.FrozenAmount-customer.Balance)
				return errTradeRejected
			}
			customer.Balance = roundMoney(customer.Balance - order.FrozenAmount)
			customer.FrozenBalance = roundMoney(customer.FrozenBalance + order.FrozenAmount)
			if err := tx.Save(&customer).Error; err != nil {
				return err
			}
		} else {
			var position system.TradePosition
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id = ? AND symbol = ? AND deleted_at IS NULL", customer.ID, security.Symbol).First(&position).Error; err != nil {
				rejection = "没有可卖持仓"
				return errTradeRejected
			}
			if position.AvailableQty < input.Quantity {
				rejection = "可卖持仓不足"
				return errTradeRejected
			}
			if input.Quantity != position.PositionQty && math.Mod(input.Quantity, 100) != 0 {
				rejection = "非全部卖出时，数量须为 100 股的整数倍"
				return errTradeRejected
			}
			position.AvailableQty -= input.Quantity
			order.FrozenQuantity = input.Quantity
			if err := tx.Save(&position).Error; err != nil {
				return err
			}
		}
		return tx.Create(&order).Error
	})
	if errors.Is(err, errTradeRejected) {
		response.ReturnError(c, response.FAILED_PRECONDITION, rejection)
		return
	}
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "提交限价委托失败")
		return
	}
	if settings.Trade.AllDay || isMobileTradingTime(time.Now(), settings.Trade.MorningStart, settings.Trade.MorningEnd, settings.Trade.AfternoonStart, settings.Trade.AfternoonEnd) {
		_, _ = matchLimitOrder(order.ID, settings)
		_ = pgdb.GetClient().First(&order, order.ID).Error
	}
	response.ReturnData(c, toLimitOrderView(order))
}

func ListLimitOrders(c *gin.Context) {
	page, pageSize := middleware.GetPage(c), middleware.GetPageSize(c)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 30
	}
	db := pgdb.GetClient().Where("customer_id = ? AND deleted_at IS NULL", middleware.GetCurrentCustomerID(c))
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		db = db.Where("status = ?", status)
	}
	var total int64
	if err := db.Model(&system.LimitOrder{}).Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取委托失败")
		return
	}
	var orders []system.LimitOrder
	if err := db.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&orders).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取委托失败")
		return
	}
	views := make([]limitOrderView, len(orders))
	for index, order := range orders {
		views[index] = toLimitOrderView(order)
	}
	response.ReturnData(c, gin.H{"records": views, "total": total, "page": page, "page_size": pageSize})
}

func CancelLimitOrder(c *gin.Context) {
	id, err := strconv.ParseUint(c.Param("id"), 10, 64)
	if err != nil || id == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "委托 ID 无效")
		return
	}
	err = pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
		var order system.LimitOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND customer_id = ?", id, middleware.GetCurrentCustomerID(c)).First(&order).Error; err != nil {
			return err
		}
		if order.Status != limitOrderPending {
			return fmt.Errorf("当前委托不可撤销")
		}
		if err := releaseLimitOrderFreeze(tx, &order); err != nil {
			return err
		}
		order.Status, order.CancelledAt = limitOrderCancelled, time.Now().Unix()
		return tx.Save(&order).Error
	})
	if err != nil {
		response.ReturnError(c, response.FAILED_PRECONDITION, err.Error())
		return
	}
	response.ReturnData(c, nil)
}

func MatchPendingLimitOrders() (int, error) {
	settings, err := loadMobileTradeSettings()
	if err != nil {
		return 0, err
	}
	if !settings.Trade.AllDay && !isMobileTradingTime(time.Now(), settings.Trade.MorningStart, settings.Trade.MorningEnd, settings.Trade.AfternoonStart, settings.Trade.AfternoonEnd) {
		return 0, nil
	}
	var ids []uint
	if err := pgdb.GetClient().Model(&system.LimitOrder{}).Where("status = ? AND deleted_at IS NULL", limitOrderPending).Order("id ASC").Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	matched := 0
	for _, id := range ids {
		ok, err := matchLimitOrder(id, settings)
		if err != nil {
			continue
		}
		if ok {
			matched++
		}
	}
	return matched, nil
}

func matchLimitOrder(id uint, settings mobileTradeSettings) (bool, error) {
	matched := false
	err := pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
		var order system.LimitOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&order, id).Error; err != nil {
			return err
		}
		if order.Status != limitOrderPending {
			return nil
		}
		var security system.StockSecurity
		if err := tx.Where("symbol = ? AND status = ?", order.Symbol, system.StatusEnabled).First(&security).Error; err != nil {
			return err
		}
		if (order.Direction == "买入" && security.LastPrice > order.LimitPrice) || (order.Direction == "卖出" && security.LastPrice < order.LimitPrice) {
			return nil
		}
		var customer system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, order.CustomerID).Error; err != nil {
			return err
		}
		if order.Direction == "买入" {
			if customer.FrozenBalance < order.FrozenAmount {
				return fmt.Errorf("冻结资金不足")
			}
			customer.FrozenBalance = roundMoney(customer.FrozenBalance - order.FrozenAmount)
			customer.Balance = roundMoney(customer.Balance + order.FrozenAmount)
		} else {
			var position system.TradePosition
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id = ? AND symbol = ? AND deleted_at IS NULL", customer.ID, order.Symbol).First(&position).Error; err != nil {
				return err
			}
			position.AvailableQty += order.FrozenQuantity
			if err := tx.Save(&position).Error; err != nil {
				return err
			}
		}
		var result orderResult
		var rejection string
		if err := executeMobileOrder(tx, &customer, security, orderInput{Code: security.Code, Direction: order.Direction, Quantity: order.Quantity}, settings, &result, &rejection); err != nil {
			return err
		}
		if err := tx.Model(&system.TradeRecord{}).Where("id = ?", result.RecordID).Update("remark", "限价委托自动成交").Error; err != nil {
			return err
		}
		order.Status, order.ExecutedPrice, order.TradeRecordID, order.FilledAt = limitOrderFilled, result.Price, result.RecordID, result.ExecutedAt
		order.FrozenAmount, order.FrozenQuantity = 0, 0
		if err := tx.Save(&order).Error; err != nil {
			return err
		}
		matched = true
		return nil
	})
	return matched, err
}

func releaseLimitOrderFreeze(tx *gorm.DB, order *system.LimitOrder) error {
	if order.Direction == "买入" {
		var customer system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, order.CustomerID).Error; err != nil {
			return err
		}
		if customer.FrozenBalance < order.FrozenAmount {
			return fmt.Errorf("冻结资金不足")
		}
		customer.FrozenBalance = roundMoney(customer.FrozenBalance - order.FrozenAmount)
		customer.Balance = roundMoney(customer.Balance + order.FrozenAmount)
		return tx.Save(&customer).Error
	}
	var position system.TradePosition
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id = ? AND symbol = ? AND deleted_at IS NULL", order.CustomerID, order.Symbol).First(&position).Error; err != nil {
		return err
	}
	position.AvailableQty += order.FrozenQuantity
	return tx.Save(&position).Error
}

func toLimitOrderView(order system.LimitOrder) limitOrderView {
	code := strings.Split(order.Symbol, ".")[0]
	labels := map[string]string{limitOrderPending: "待成交", limitOrderFilled: "已成交", limitOrderCancelled: "已撤单"}
	return limitOrderView{ID: order.ID, Code: code, Symbol: order.Symbol, StockName: order.StockName, Direction: order.Direction, LimitPrice: order.LimitPrice, Quantity: order.Quantity, Status: order.Status, StatusLabel: labels[order.Status], FrozenAmount: order.FrozenAmount, ExecutedPrice: order.ExecutedPrice, CreatedAt: order.CreatedAt.Unix(), FilledAt: order.FilledAt, CancelledAt: order.CancelledAt}
}
