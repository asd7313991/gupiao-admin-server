package mobile

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"api-server/util/hkquote"
	"api-server/util/positionbasis"
	"api-server/util/positionquote"
)

type marginCallRequirement struct {
	position *system.TradePosition
	from     int
	to       int
	each     float64
}

func ProcessMarginCalls(now time.Time) (supplements int, forced int, err error) {
	settings, err := loadMobileTradeSettings()
	if err != nil {
		return 0, 0, err
	}
	if settings.Risk.MarginCallStart <= 0 || settings.Risk.MarginCallRate <= 0 {
		return 0, 0, nil
	}
	var customerIDs []uint
	if err := pgdb.GetClient().Model(&system.TradePosition{}).
		Where("position_qty > 0 AND deleted_at IS NULL").
		Distinct("customer_id").Pluck("customer_id", &customerIDs).Error; err != nil {
		return 0, 0, err
	}
	var failures error
	for _, customerID := range customerIDs {
		for _, market := range []string{"A", "HK"} {
			count, wasForced, processErr := processCustomerMarginCallsForMarketDB(pgdb.GetClient(), customerID, settings.Risk.MarginCallStart, settings.Risk.MarginCallRate, now, market)
			if processErr != nil {
				failures = errors.Join(failures, fmt.Errorf("客户%d %s 风险处理失败: %w", customerID, market, processErr))
				continue
			}
			supplements += count
			if wasForced {
				forced++
			}
		}
	}
	return supplements, forced, failures
}

func processCustomerMarginCalls(customerID uint, startLoss, supplementRate float64, now time.Time) (int, bool, error) {
	return processCustomerMarginCallsDB(pgdb.GetClient(), customerID, startLoss, supplementRate, now)
}

func processCustomerMarginCallsDB(db *gorm.DB, customerID uint, startLoss, supplementRate float64, now time.Time) (int, bool, error) {
	return processCustomerMarginCallsForMarketDB(db, customerID, startLoss, supplementRate, now, "A")
}

func processCustomerMarginCallsForMarketDB(db *gorm.DB, customerID uint, startLoss, supplementRate float64, now time.Time, market string) (int, bool, error) {
	var quotes map[string]positionquote.Quote
	if market == "HK" {
		var symbols []string
		if err := db.Model(&system.TradePosition{}).Where("customer_id = ? AND position_qty > 0 AND symbol LIKE ? AND currency = ? AND leverage = ? AND status = ?", customerID, "%.HK", "HKD", positionbasis.FinancingLeverage, system.StatusEnabled).Pluck("symbol", &symbols).Error; err != nil {
			return 0, false, err
		}
		if len(symbols) == 0 {
			return 0, false, nil
		}
		var err error
		quotes, err = positionquote.FetchAvailable(context.Background(), &http.Client{Timeout: 8 * time.Second}, "https://qt.gtimg.cn/q=", hkquote.EastmoneyURL, symbols, now)
		if err != nil {
			return 0, false, fmt.Errorf("港股风险检查报价不可用，未补仓或强平: %w", err)
		}
	}
	return processCustomerMarginCallsWithQuotesDB(db, customerID, startLoss, supplementRate, now, market, quotes)
}

func processCustomerMarginCallsWithQuotesDB(db *gorm.DB, customerID uint, startLoss, supplementRate float64, now time.Time, market string, quotes map[string]positionquote.Quote) (int, bool, error) {
	supplements, forced := 0, false
	err := db.Transaction(func(tx *gorm.DB) error {
		var customer system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, customerID).Error; err != nil {
			return err
		}
		var positions []system.TradePosition
		query := tradeMarketScope(tx.Clauses(clause.Locking{Strength: "UPDATE"}), market).
			Where("customer_id = ? AND position_qty > 0 AND deleted_at IS NULL", customerID)
		if market == "HK" {
			query = query.Where("currency = ? AND leverage = ? AND status = ?", "HKD", positionbasis.FinancingLeverage, system.StatusEnabled)
		}
		if err := query.Find(&positions).Error; err != nil {
			return err
		}
		balance, currency := &customer.Balance, "CNY"
		if market == "HK" {
			balance, currency = &customer.HKDBalance, "HKD"
		}
		requirements := make([]marginCallRequirement, 0)
		totalRequired := float64(0)
		for index := range positions {
			position := &positions[index]
			if market == "HK" {
				quote, ok := quotes[position.Symbol]
				if !ok || quote.Price <= 0 || quote.At <= 0 {
					return fmt.Errorf("%s 缺少有效风险报价，未补仓或强平", position.Symbol)
				}
				position.CurrentPrice = quote.Price
				position.MarketValue, position.ProfitLoss, position.ProfitRate = positionbasis.Values(*position, quote.Price)
			} else {
				refreshPositionMarketValue(tx, position)
			}
			priceDropRate := positionPriceDropRate(*position)
			from, to, ok := pendingMarginCallRange(priceDropRate, startLoss, position.MarginCallLevel)
			if !ok {
				continue
			}
			each := roundMoney(position.MarketValue * supplementRate)
			if each <= 0 {
				continue
			}
			requirements = append(requirements, marginCallRequirement{position: position, from: from, to: to, each: each})
			totalRequired = roundMoney(totalRequired + each*float64(to-from+1))
		}
		if len(requirements) == 0 {
			return nil
		}
		if *balance < totalRequired {
			if err := cancelCustomerPendingOrdersForMarket(tx, &customer, customerID, now.Unix(), market); err != nil {
				return err
			}
		}
		if *balance < totalRequired {
			forced = true
			if err := forceCloseAllPositions(tx, &customer, positions, now.Unix(), "风险补仓资金不足，系统全部平仓"); err != nil {
				return err
			}
			if market != "HK" {
				customer.Balance = roundMoney(math.Max(0, customer.Balance))
			}
			return tx.Save(&customer).Error
		}
		for _, requirement := range requirements {
			_, supplemental := positionbasis.MarginParts(*requirement.position)
			for level := requirement.from; level <= requirement.to; level++ {
				*balance = roundMoney(*balance - requirement.each)
				requirement.position.Margin = roundMoney(requirement.position.Margin + requirement.each)
				supplemental = roundMoney(supplemental + requirement.each)
				positionbasis.SetSupplementalMargin(requirement.position, supplemental)
				remark := fmt.Sprintf("%s 亏损达到 %d%%，按市值 %.2f 的 %.2f%% 补充担保金", requirement.position.StockName, level, requirement.position.MarketValue, supplementRate*100)
				if err := tx.Create(&system.CustomerFundRecord{CustomerID: customer.ID, Type: "风险补仓", Direction: "扣款", Currency: currency, Amount: requirement.each, Balance: *balance, Remark: remark}).Error; err != nil {
					return err
				}
				supplements++
			}
			requirement.position.MarginCallLevel = requirement.to
			if err := tx.Model(requirement.position).Updates(map[string]any{
				"current_price":       requirement.position.CurrentPrice,
				"market_value":        requirement.position.MarketValue,
				"profit_loss":         requirement.position.ProfitLoss,
				"profit_rate":         requirement.position.ProfitRate,
				"margin":              requirement.position.Margin,
				"supplemental_margin": requirement.position.SupplementalMargin,
				"margin_call_level":   requirement.position.MarginCallLevel,
			}).Error; err != nil {
				return err
			}
		}
		return tx.Save(&customer).Error
	})
	return supplements, forced, err
}

func refreshPositionMarketValue(tx *gorm.DB, position *system.TradePosition) {
	price := position.CurrentPrice
	var security system.StockSecurity
	if tx.Where("symbol = ?", position.Symbol).First(&security).Error == nil && security.LastPrice > 0 {
		price = security.LastPrice
	}
	position.CurrentPrice = price
	position.MarketValue, position.ProfitLoss, position.ProfitRate = positionbasis.Values(*position, price)
}

func positionPriceDropRate(position system.TradePosition) float64 {
	if position.CostPrice <= 0 || position.CurrentPrice >= position.CostPrice {
		return 0
	}
	return (position.CostPrice - position.CurrentPrice) / position.CostPrice * 100
}

func pendingMarginCallRange(priceDropRate, startLoss float64, lastLevel int) (from, to int, ok bool) {
	startLevel := int(math.Floor(startLoss))
	to = int(math.Floor(priceDropRate + 1e-9))
	if lastLevel < startLevel-1 {
		lastLevel = startLevel - 1
	}
	if to <= lastLevel {
		return 0, 0, false
	}
	return lastLevel + 1, to, true
}
