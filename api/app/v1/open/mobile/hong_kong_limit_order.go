package mobile

import (
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

type hongKongLimitInput struct {
	limitOrderInput
	RequestID string `json:"request_id"`
}

func parseHongKongDepthLevel(rawPrice, rawVolume string) orderBookLevel {
	price, priceErr := strconv.ParseFloat(rawPrice, 64)
	volume, volumeErr := strconv.ParseFloat(rawVolume, 64)
	if priceErr != nil || volumeErr != nil || math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 || price > 1_000_000 ||
		math.IsNaN(volume) || math.IsInf(volume, 0) || volume <= 0 || volume != math.Trunc(volume) {
		return orderBookLevel{}
	}
	return orderBookLevel{Price: price, Volume: volume}
}

func validateHongKongLimitMetadata(quote hongKongQuote, security system.StockSecurity, settings mobileTradeSettings, now time.Time) string {
	if message := validateHongKongSettings(settings); message != "" {
		return message
	}
	if !isHongKongSecurity(security) || security.Status != system.StatusEnabled || security.Code != quote.Code {
		return "港股证券不可交易"
	}
	// 挂单可在收市后提交，只用报价确认币种、普通股及每手股数；成交必须另行验证时段和盘口时效。
	return validateHongKongQuoteWithCloseTest(quote, now, true)
}

func validateHongKongLimitInput(input hongKongLimitInput) string {
	if !exchangeRequestPattern.MatchString(input.RequestID) || (input.Direction != "买入" && input.Direction != "卖出") ||
		math.IsNaN(input.Quantity) || math.IsInf(input.Quantity, 0) || input.Quantity <= 0 || input.Quantity > 100_000_000 || input.Quantity != math.Trunc(input.Quantity) {
		return "港股请求编号、方向或数量无效"
	}
	if math.IsNaN(input.LimitPrice) || math.IsInf(input.LimitPrice, 0) || input.LimitPrice < 0.001 || input.LimitPrice > 1_000_000 ||
		math.Abs(input.LimitPrice*1000-math.Round(input.LimitPrice*1000)) > 0.000001 || roundMoney(input.Quantity*input.LimitPrice) <= 0 ||
		roundMoney(input.Quantity*input.LimitPrice) > 1_000_000_000 {
		return "港股目标价最多三位小数，委托金额须在模拟交易限制内"
	}
	return ""
}

func replayHongKongLimitOrder(order system.LimitOrder, input hongKongLimitInput) error {
	if order.Symbol != input.Code+".HK" || order.Direction != input.Direction || order.Quantity != input.Quantity || order.LimitPrice != input.LimitPrice {
		return exchangeRejection("相同请求编号不能用于不同的港股限价委托")
	}
	return nil
}

func PlaceHongKongLimitOrder(c *gin.Context) {
	var input hongKongLimitInput
	if err := c.ShouldBindJSON(&input); err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "港股委托参数无效")
		return
	}
	input.Code = strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(input.Code)), ".HK")
	if message := validateHongKongLimitInput(input); message != "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, message)
		return
	}
	customerID := middleware.GetCurrentCustomerID(c)
	var existing system.LimitOrder
	err := pgdb.GetClient().Where("customer_id = ? AND request_id = ?", customerID, input.RequestID).First(&existing).Error
	if err == nil {
		if err := replayHongKongLimitOrder(existing, input); err != nil {
			response.ReturnError(c, response.FAILED_PRECONDITION, err.Error())
			return
		}
		response.ReturnData(c, toLimitOrderView(existing))
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		response.ReturnError(c, response.DATA_LOSS, "读取港股委托失败，请使用原编号重试")
		return
	}
	security, ok := findSecurity(input.Code)
	if !ok || !isHongKongSecurity(security) {
		response.ReturnError(c, response.NOT_FOUND, "港股证券不存在")
		return
	}
	quote, err := fetchHongKongTradeQuote(c.Request.Context(), security.Code)
	if err != nil {
		zap.L().Warn("读取港股委托基础行情失败", zap.Error(err))
		response.ReturnError(c, response.UNAVAILABLE, "读取港股每手股数及币种失败，请使用原编号重试")
		return
	}
	order, err := placeHongKongLimitOrderDB(pgdb.GetClient(), customerID, security, quote, input, time.Now())
	if err != nil {
		var rejection exchangeRejection
		if errors.As(err, &rejection) {
			response.ReturnError(c, response.FAILED_PRECONDITION, rejection.Error())
		} else {
			zap.L().Error("保存港股限价委托失败", zap.Error(err))
			response.ReturnError(c, response.DATA_LOSS, "保存港股委托失败，请使用原编号确认结果")
		}
		return
	}
	response.ReturnData(c, toLimitOrderView(order))
}

func placeHongKongLimitOrderDB(db *gorm.DB, customerID uint, security system.StockSecurity, quote hongKongQuote, input hongKongLimitInput, now time.Time) (system.LimitOrder, error) {
	var order system.LimitOrder
	err := db.Transaction(func(tx *gorm.DB) error {
		if message := validateHongKongLimitInput(input); message != "" {
			return exchangeRejection(message)
		}
		var customer system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, customerID).Error; err != nil {
			return err
		}
		err := tx.Where("customer_id = ? AND request_id = ?", customerID, input.RequestID).First(&order).Error
		if err == nil {
			return replayHongKongLimitOrder(order, input)
		}
		if !errors.Is(err, gorm.ErrRecordNotFound) {
			return err
		}
		if customer.Status != system.StatusEnabled || customer.FundStatus != system.StatusEnabled || customer.Verified != system.StatusEnabled {
			return exchangeRejection("账户未完成认证或资金账户不可用")
		}
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&security, security.ID).Error; err != nil {
			return err
		}
		var setting system.AppSystemSetting
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&setting).Error; err != nil {
			return err
		}
		settings, err := parseMobileTradeSettings(setting.Config)
		if err != nil {
			return err
		}
		if message := validateHongKongLimitMetadata(quote, security, settings, now); message != "" {
			return exchangeRejection(message)
		}
		if input.Code != security.Code {
			return exchangeRejection("委托证券代码不匹配")
		}
		order = system.LimitOrder{CustomerID: customerID, RequestID: &input.RequestID, Symbol: security.Symbol, StockName: security.Name,
			Direction: input.Direction, LimitPrice: input.LimitPrice, Quantity: input.Quantity, Status: limitOrderPending}
		if input.Direction == "买入" {
			if math.Mod(input.Quantity, quote.LotSize) != 0 {
				return exchangeRejection(fmt.Sprintf("该港股每手 %.0f 股，买入数量必须为整手", quote.LotSize))
			}
			security.LastPrice = input.LimitPrice
			amount, _, _, _, _, fees := calculateTradeFees(security, input.Quantity, input.Direction, settings)
			order.FrozenAmount = roundMoney(amount + fees)
			if customer.HKDBalance < order.FrozenAmount {
				return exchangeRejection("可用港币不足以冻结本金和预估费用")
			}
			customer.HKDBalance = roundMoney(customer.HKDBalance - order.FrozenAmount)
			customer.HKDFrozenBalance = roundMoney(customer.HKDFrozenBalance + order.FrozenAmount)
			if err := tx.Model(&customer).Updates(map[string]any{"hkd_balance": customer.HKDBalance, "hkd_frozen_balance": customer.HKDFrozenBalance}).Error; err != nil {
				return err
			}
		} else {
			var position system.TradePosition
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id = ? AND symbol = ?", customerID, security.Symbol).First(&position).Error; err != nil {
				if errors.Is(err, gorm.ErrRecordNotFound) {
					return exchangeRejection("没有港股可卖持仓")
				}
				return err
			}
			if position.Currency != "HKD" || position.AvailableQty < input.Quantity ||
				(input.Quantity != position.AvailableQty && math.Mod(input.Quantity, quote.LotSize) != 0) {
				return exchangeRejection("港股可卖持仓不足，非全部卖出时须为整手")
			}
			position.AvailableQty -= input.Quantity
			order.FrozenQuantity = input.Quantity
			if err := tx.Save(&position).Error; err != nil {
				return err
			}
		}
		if err := tx.Create(&order).Error; err != nil {
			return err
		}
		if order.Direction == "买入" {
			return writeHongKongLimitFreezeFlow(tx, customer, order, false)
		}
		return nil
	})
	return order, err
}

func writeHongKongLimitFreezeFlow(tx *gorm.DB, customer system.Customer, order system.LimitOrder, release bool) error {
	kind, direction := "港股委托冻结", "扣款"
	if release {
		kind, direction = "港股委托解冻", "入账"
	}
	return tx.Create(&system.CustomerFundRecord{CustomerID: customer.ID, Type: kind, Direction: direction, Currency: "HKD",
		Amount: order.FrozenAmount, Balance: customer.HKDBalance, Remark: fmt.Sprintf("港股模拟限价委托 %d；%s %s %.0f股，目标价 %.3f HKD", order.ID, order.StockName, order.Direction, order.Quantity, order.LimitPrice)}).Error
}

func releaseHongKongLimitFreeze(tx *gorm.DB, customer *system.Customer, order system.LimitOrder) error {
	if order.Direction == "买入" {
		if order.FrozenAmount <= 0 || customer.HKDFrozenBalance < order.FrozenAmount {
			return fmt.Errorf("港币冻结资金异常，未执行解冻")
		}
		customer.HKDFrozenBalance = roundMoney(customer.HKDFrozenBalance - order.FrozenAmount)
		customer.HKDBalance = roundMoney(customer.HKDBalance + order.FrozenAmount)
		if err := tx.Model(customer).Updates(map[string]any{"hkd_balance": customer.HKDBalance, "hkd_frozen_balance": customer.HKDFrozenBalance}).Error; err != nil {
			return err
		}
		return writeHongKongLimitFreezeFlow(tx, *customer, order, true)
	}
	var position system.TradePosition
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id = ? AND symbol = ?", customer.ID, order.Symbol).First(&position).Error; err != nil {
		return err
	}
	if position.Currency != "HKD" || order.FrozenQuantity != order.Quantity || position.AvailableQty+order.FrozenQuantity > position.PositionQty {
		return fmt.Errorf("港股冻结持仓异常，未执行解冻")
	}
	position.AvailableQty += order.FrozenQuantity
	return tx.Save(&position).Error
}

func cancelHongKongLimitOrderDB(db *gorm.DB, customerID, id uint, now time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error {
		var customer system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, customerID).Error; err != nil {
			return err
		}
		var order system.LimitOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND customer_id = ? AND symbol LIKE ?", id, customerID, "%.HK").First(&order).Error; err != nil {
			return err
		}
		if order.Status == limitOrderCancelled {
			return nil
		}
		if order.Status != limitOrderPending {
			return exchangeRejection("已成交委托不能撤销")
		}
		if err := releaseHongKongLimitFreeze(tx, &customer, order); err != nil {
			return err
		}
		order.Status, order.CancelledAt = limitOrderCancelled, now.Unix()
		order.FrozenAmount, order.FrozenQuantity = 0, 0
		return tx.Save(&order).Error
	})
}

func hongKongLimitMatchPrice(order system.LimitOrder, quote hongKongQuote, now time.Time, settings mobileTradeSettings) (float64, bool) {
	quote = hongKongDepthQuote(quote)
	if !hongKongTradeTime(now, settings) || validateHongKongQuote(quote, now) != "" || order.Symbol != quote.Code+".HK" {
		return 0, false
	}
	bestBid, bestAsk := 0.0, 0.0
	for _, level := range quote.Bid {
		if validHongKongDepthLevel(level) && level.Price > bestBid {
			bestBid = level.Price
		}
	}
	for _, level := range quote.Ask {
		if validHongKongDepthLevel(level) && (bestAsk == 0 || level.Price < bestAsk) {
			bestAsk = level.Price
		}
	}
	if bestAsk > 0 && bestBid > bestAsk {
		return 0, false
	}
	if order.Direction == "买入" && bestAsk > 0 && bestAsk <= order.LimitPrice {
		return bestAsk, true
	}
	if order.Direction == "卖出" && bestBid > 0 && bestBid >= order.LimitPrice {
		return bestBid, true
	}
	return 0, false
}

func hongKongDepthQuote(quote hongKongQuote) hongKongQuote {
	if quote.DepthAt > 0 {
		quote.QuoteAt, quote.Source = quote.DepthAt, "tencent"
	} else if quote.Source == "eastmoney" {
		quote.QuoteAt = 0
	}
	return quote
}

func validHongKongDepthLevel(level orderBookLevel) bool {
	return !math.IsNaN(level.Price) && !math.IsInf(level.Price, 0) && level.Price > 0 && level.Price <= 1_000_000 &&
		math.Abs(level.Price*1000-math.Round(level.Price*1000)) < 0.000001 &&
		!math.IsNaN(level.Volume) && !math.IsInf(level.Volume, 0) && level.Volume > 0 && level.Volume == math.Trunc(level.Volume)
}

func matchHongKongLimitOrderDB(db *gorm.DB, customerID, id uint, quote hongKongQuote, now time.Time) (bool, error) {
	quote = hongKongDepthQuote(quote)
	filled := false
	err := db.Transaction(func(tx *gorm.DB) error {
		var customer system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, customerID).Error; err != nil {
			return err
		}
		var order system.LimitOrder
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("id = ? AND customer_id = ? AND symbol LIKE ?", id, customerID, "%.HK").First(&order).Error; err != nil {
			return err
		}
		if order.Status != limitOrderPending {
			return nil
		}
		var setting system.AppSystemSetting
		if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&setting).Error; err != nil {
			return err
		}
		settings, err := parseMobileTradeSettings(setting.Config)
		if err != nil {
			return err
		}
		price, match := hongKongLimitMatchPrice(order, quote, now, settings)
		if !match {
			return nil
		}
		var security system.StockSecurity
		if err := tx.Where("symbol = ?", order.Symbol).First(&security).Error; err != nil {
			return err
		}
		security.LastPrice = price
		if order.Direction == "买入" {
			amount, _, _, _, _, fee := calculateTradeFees(security, order.Quantity, order.Direction, settings)
			if roundMoney(amount+fee) > order.FrozenAmount {
				return exchangeRejection("当前费用超过原冻结港币，委托保留待成交，请撤单后重新提交")
			}
		}
		if err := releaseHongKongLimitFreeze(tx, &customer, order); err != nil {
			return err
		}
		quote.LastPrice = price
		input := hongKongOrderInput{orderInput: orderInput{Code: quote.Code, Direction: order.Direction, Quantity: order.Quantity},
			RequestID: fmt.Sprintf("limit:%d", order.ID), LimitOrderID: order.ID}
		result, err := executeHongKongOrder(tx, &customer, security, quote, input, now, false)
		if err != nil {
			return err
		}
		order.Status, order.ExecutedPrice, order.TradeRecordID, order.FilledAt, order.QuoteAt = limitOrderFilled, result.Price, result.RecordID, result.ExecutedAt, quote.QuoteAt
		order.FrozenAmount, order.FrozenQuantity = 0, 0
		if err := tx.Save(&order).Error; err != nil {
			return err
		}
		filled = true
		return nil
	})
	return filled && err == nil, err
}
