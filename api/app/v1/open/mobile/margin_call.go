package mobile

import (
	"fmt"
	"math"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
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
	for _, customerID := range customerIDs {
		count, wasForced, processErr := processCustomerMarginCalls(customerID, settings.Risk.MarginCallStart, settings.Risk.MarginCallRate, now)
		if processErr != nil {
			return supplements, forced, processErr
		}
		supplements += count
		if wasForced {
			forced++
		}
	}
	return supplements, forced, nil
}

func processCustomerMarginCalls(customerID uint, startLoss, supplementRate float64, now time.Time) (int, bool, error) {
	supplements, forced := 0, false
	err := pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
		var customer system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, customerID).Error; err != nil {
			return err
		}
		var positions []system.TradePosition
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id = ? AND position_qty > 0 AND deleted_at IS NULL", customerID).Find(&positions).Error; err != nil {
			return err
		}
		requirements := make([]marginCallRequirement, 0)
		totalRequired := float64(0)
		for index := range positions {
			position := &positions[index]
			refreshPositionMarketValue(tx, position)
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
		if customer.Balance < totalRequired {
			if err := cancelCustomerPendingOrders(tx, &customer, customerID, now.Unix()); err != nil {
				return err
			}
		}
		if customer.Balance < totalRequired {
			forced = true
			if err := forceCloseAllPositions(tx, &customer, positions, now.Unix(), "风险补仓资金不足，系统全部平仓"); err != nil {
				return err
			}
			customer.Balance = roundMoney(math.Max(0, customer.Balance))
			return tx.Save(&customer).Error
		}
		for _, requirement := range requirements {
			for level := requirement.from; level <= requirement.to; level++ {
				customer.Balance = roundMoney(customer.Balance - requirement.each)
				requirement.position.Margin = roundMoney(requirement.position.Margin + requirement.each)
				remark := fmt.Sprintf("%s 亏损达到 %d%%，按市值 %.2f 的 %.2f%% 补充担保金", requirement.position.StockName, level, requirement.position.MarketValue, supplementRate*100)
				if err := tx.Create(&system.CustomerFundRecord{CustomerID: customer.ID, Type: "风险补仓", Direction: "扣款", Currency: "CNY", Amount: requirement.each, Balance: customer.Balance, Remark: remark}).Error; err != nil {
					return err
				}
				supplements++
			}
			requirement.position.MarginCallLevel = requirement.to
			if err := tx.Model(requirement.position).Updates(map[string]any{
				"current_price":     requirement.position.CurrentPrice,
				"market_value":      requirement.position.MarketValue,
				"profit_loss":       requirement.position.ProfitLoss,
				"profit_rate":       requirement.position.ProfitRate,
				"margin":            requirement.position.Margin,
				"margin_call_level": requirement.position.MarginCallLevel,
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
	position.MarketValue, position.ProfitLoss, position.ProfitRate = calculatePositionValues(position.PositionQty, price, position.TotalCost, positionLeverage(*position))
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
