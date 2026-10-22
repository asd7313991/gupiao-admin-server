package mobile

import (
	"context"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
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
	Currency      string  `json:"currency"`
	QuoteAt       int64   `json:"quote_at"`
	MatchingNote  string  `json:"matching_note,omitempty"`
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
	if !ok || security.Status != system.StatusEnabled {
		response.ReturnError(c, response.NOT_FOUND, "证券不可交易")
		return
	}
	if isHongKongSecurity(security) {
		response.ReturnError(c, response.FAILED_PRECONDITION, "港股限价委托请使用独立港股接口")
		return
	}
	book, err := fetchOrderBook(security)
	if err != nil || book.LastPrice <= 0 {
		response.ReturnError(c, response.UNAVAILABLE, "盘口数据暂时不可用，请稍后重试")
		return
	}
	security.LastPrice = book.LastPrice
	if message := validateLimitOrderPrice(input, book); message != "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, message)
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
			securityAtPrice := security
			securityAtPrice.LastPrice = input.LimitPrice
			amount, _, _, _, _, fee := calculateTradeFees(securityAtPrice, input.Quantity, input.Direction, settings)
			order.FrozenAmount = roundMoney(amount + fee)
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
	db = tradeMarketScope(db, c.Query("market"))
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
	var snapshot system.LimitOrder
	if err := pgdb.GetClient().Where("id = ? AND customer_id = ?", id, middleware.GetCurrentCustomerID(c)).First(&snapshot).Error; err != nil {
		response.ReturnError(c, response.NOT_FOUND, "委托不存在")
		return
	}
	if strings.HasSuffix(snapshot.Symbol, ".HK") {
		if err := cancelHongKongLimitOrderDB(pgdb.GetClient(), snapshot.CustomerID, snapshot.ID, time.Now()); err != nil {
			zap.L().Warn("撤销港股限价委托失败", zap.Uint("order_id", snapshot.ID), zap.Error(err))
			response.ReturnError(c, response.FAILED_PRECONDITION, err.Error())
			return
		}
		response.ReturnData(c, nil)
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
	var ids []uint
	if err := pgdb.GetClient().Model(&system.LimitOrder{}).Where("status = ? AND deleted_at IS NULL", limitOrderPending).Order("id ASC").Pluck("id", &ids).Error; err != nil {
		return 0, err
	}
	matched := 0
	var failures []error
	for _, id := range ids {
		ok, err := matchLimitOrder(id, settings)
		if err != nil {
			zap.L().Warn("限价委托撮合失败", zap.Uint("order_id", id), zap.Error(err))
			failures = append(failures, fmt.Errorf("委托 %d: %w", id, err))
			continue
		}
		if ok {
			matched++
		}
	}
	return matched, errors.Join(failures...)
}

func matchLimitOrder(id uint, settings mobileTradeSettings) (bool, error) {
	var snapshot system.LimitOrder
	if err := pgdb.GetClient().First(&snapshot, id).Error; err != nil {
		return false, err
	}
	if snapshot.Status != limitOrderPending {
		return false, nil
	}
	if strings.HasSuffix(snapshot.Symbol, ".HK") {
		now := time.Now()
		if !hongKongTradeTime(now, settings) || !settings.HKTrade.Enabled {
			return false, nil
		}
		quote, err := fetchHongKongTradeQuote(context.Background(), strings.TrimSuffix(snapshot.Symbol, ".HK"))
		if err != nil {
			return false, err
		}
		return matchHongKongLimitOrderDB(pgdb.GetClient(), snapshot.CustomerID, id, quote, now)
	}
	if !settings.Trade.AllDay && !isMobileTradingTime(time.Now(), settings.Trade.MorningStart, settings.Trade.MorningEnd, settings.Trade.AfternoonStart, settings.Trade.AfternoonEnd) {
		return false, nil
	}
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
		matchPrice, matched := limitOrderMatchPrice(order, security)
		if !matched {
			return nil
		}
		security.LastPrice = matchPrice
		tradeInput := orderInput{Code: security.Code, Direction: order.Direction, Quantity: order.Quantity}
		if message := validateSecurityTrade(security, tradeInput, settings); message != "" {
			if err := releaseLimitOrderFreeze(tx, &order); err != nil {
				return err
			}
			order.Status, order.CancelledAt = limitOrderCancelled, time.Now().Unix()
			return tx.Save(&order).Error
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
		if err := executeMobileOrder(tx, &customer, security, tradeInput, settings, &result, &rejection); err != nil {
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

func limitOrderMatchPrice(order system.LimitOrder, security system.StockSecurity) (float64, bool) {
	book, err := fetchOrderBook(security)
	if err != nil {
		return 0, false
	}
	return matchPriceFromOrderBook(order, book)
}

func matchPriceFromOrderBook(order system.LimitOrder, book orderBookView) (float64, bool) {
	if order.Direction == "买入" {
		for _, level := range book.Ask {
			if level.Price > 0 && level.Volume > 0 && level.Price <= order.LimitPrice {
				return level.Price, true
			}
		}
		return 0, false
	}
	for _, level := range book.Bid {
		if level.Price > 0 && level.Volume > 0 && level.Price >= order.LimitPrice {
			return level.Price, true
		}
	}
	return 0, false
}

func validateLimitOrderPrice(input limitOrderInput, book orderBookView) string {
	if book.LimitDown <= 0 || book.LimitUp <= 0 {
		return ""
	}
	if input.LimitPrice < book.LimitDown || input.LimitPrice > book.LimitUp {
		return fmt.Sprintf("委托价格必须在 %.2f 至 %.2f 之间", book.LimitDown, book.LimitUp)
	}
	return ""
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
	currency, note := "CNY", ""
	if strings.HasSuffix(order.Symbol, ".HK") {
		currency = "HKD"
		if order.Status == limitOrderPending {
			note = "按有效买卖盘口模拟成交，交易时段限制由后台全天交易开关控制；无盘口、零挂量、过期行情或未达限价时继续等待，可撤单解冻"
		}
	}
	return limitOrderView{ID: order.ID, Code: code, Symbol: order.Symbol, StockName: order.StockName, Direction: order.Direction, LimitPrice: order.LimitPrice, Quantity: order.Quantity, Status: order.Status, StatusLabel: labels[order.Status], FrozenAmount: order.FrozenAmount, ExecutedPrice: order.ExecutedPrice, CreatedAt: order.CreatedAt.Unix(), FilledAt: order.FilledAt, CancelledAt: order.CancelledAt, Currency: currency, QuoteAt: order.QuoteAt, MatchingNote: note}
}
