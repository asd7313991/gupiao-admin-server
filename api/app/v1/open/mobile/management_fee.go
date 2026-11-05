package mobile

import (
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"api-server/util/positionbasis"
)

func ProcessDailyManagementFees(now time.Time) (processed int, forced int, err error) {
	settings, err := loadMobileTradeSettings()
	if err != nil {
		return 0, 0, err
	}
	rate := effectiveManagementFeeRate(settings)
	if rate <= 0 {
		return 0, 0, nil
	}
	chargeDate := now.In(time.FixedZone("Asia/Shanghai", 8*60*60)).Format("2006-01-02")
	var customerIDs []uint
	if err := pgdb.GetClient().Model(&system.TradePosition{}).
		Where("position_qty > 0 AND deleted_at IS NULL AND symbol NOT LIKE ?", "%.HK").
		Distinct("customer_id").Pluck("customer_id", &customerIDs).Error; err != nil {
		return 0, 0, err
	}
	for _, customerID := range customerIDs {
		wasForced, processErr := processCustomerManagementFee(customerID, chargeDate, rate, now)
		if errors.Is(processErr, gorm.ErrDuplicatedKey) {
			continue
		}
		if processErr != nil {
			return processed, forced, processErr
		}
		processed++
		if wasForced {
			forced++
		}
	}
	return processed, forced, nil
}

func processCustomerManagementFee(customerID uint, chargeDate string, rate float64, now time.Time) (bool, error) {
	return processCustomerManagementFeeDB(pgdb.GetClient(), customerID, chargeDate, rate, now)
}

func processCustomerManagementFeeDB(db *gorm.DB, customerID uint, chargeDate string, rate float64, now time.Time) (bool, error) {
	forced := false
	err := db.Transaction(func(tx *gorm.DB) error {
		var existing int64
		if err := tx.Model(&system.DailyManagementFee{}).Where("customer_id = ? AND charge_date = ?", customerID, chargeDate).Count(&existing).Error; err != nil {
			return err
		}
		if existing > 0 {
			return gorm.ErrDuplicatedKey
		}
		var customer system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, customerID).Error; err != nil {
			return err
		}
		var positions []system.TradePosition
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id = ? AND position_qty > 0 AND deleted_at IS NULL AND symbol NOT LIKE ?", customerID, "%.HK").Find(&positions).Error; err != nil {
			return err
		}
		marketValue := currentPositionsMarketValue(tx, positions)
		fee := dailyManagementFee(marketValue, rate)
		if fee <= 0 {
			return nil
		}
		if customer.Balance < fee {
			if err := cancelCustomerPendingOrders(tx, &customer, customerID, now.Unix()); err != nil {
				return err
			}
		}
		if customer.Balance < fee {
			forced = true
			if err := forceCloseAllPositions(tx, &customer, positions, now.Unix(), "管理费余额不足，系统全部平仓"); err != nil {
				return err
			}
		}
		charged := math.Min(math.Max(customer.Balance, 0), fee)
		customer.Balance = roundMoney(math.Max(0, customer.Balance-charged))
		if err := tx.Save(&customer).Error; err != nil {
			return err
		}
		if err := tx.Create(&system.CustomerFundRecord{CustomerID: customer.ID, Type: "持仓管理费", Direction: "扣款", Currency: "CNY", Amount: charged, Balance: customer.Balance, Remark: fmt.Sprintf("%s 持仓市值 %.2f，管理费率 %.6f", chargeDate, marketValue, rate)}).Error; err != nil {
			return err
		}
		return tx.Create(&system.DailyManagementFee{CustomerID: customer.ID, ChargeDate: chargeDate, MarketValue: marketValue, FeeAmount: fee, ChargedAmount: charged, ForcedClose: forced, BalanceAfter: customer.Balance}).Error
	})
	return forced, err
}

func currentPositionsMarketValue(tx *gorm.DB, positions []system.TradePosition) float64 {
	total := float64(0)
	for index := range positions {
		position := &positions[index]
		price := position.CurrentPrice
		var security system.StockSecurity
		if tx.Where("symbol = ?", position.Symbol).First(&security).Error == nil && security.LastPrice > 0 {
			price = security.LastPrice
		}
		position.CurrentPrice = price
		position.MarketValue, position.ProfitLoss, position.ProfitRate = positionbasis.Values(*position, price)
		total += position.MarketValue
	}
	return roundMoney(total)
}

func dailyManagementFee(marketValue, rate float64) float64 {
	return roundMoney(marketValue * rate)
}

func cancelCustomerPendingOrders(tx *gorm.DB, customer *system.Customer, customerID uint, cancelledAt int64) error {
	return cancelCustomerPendingOrdersForMarket(tx, customer, customerID, cancelledAt, "A")
}

func cancelCustomerPendingOrdersForMarket(tx *gorm.DB, customer *system.Customer, customerID uint, cancelledAt int64, market string) error {
	var orders []system.LimitOrder
	if err := tradeMarketScope(tx.Clauses(clause.Locking{Strength: "UPDATE"}), market).Where("customer_id = ? AND status = ? AND deleted_at IS NULL", customerID, limitOrderPending).Find(&orders).Error; err != nil {
		return err
	}
	for index := range orders {
		order := &orders[index]
		if market == "HK" {
			if err := releaseHongKongLimitFreeze(tx, customer, *order); err != nil {
				return err
			}
		} else if order.Direction == "买入" {
			customer.FrozenBalance = roundMoney(math.Max(0, customer.FrozenBalance-order.FrozenAmount))
			customer.Balance = roundMoney(customer.Balance + order.FrozenAmount)
		} else {
			if err := releaseAStockPositionFreeze(tx, *order); err != nil {
				return err
			}
		}
		order.Status, order.CancelledAt = limitOrderCancelled, cancelledAt
		order.FrozenAmount, order.FrozenQuantity = 0, 0
		if err := tx.Save(order).Error; err != nil {
			return err
		}
	}
	return nil
}

func forceCloseAllPositions(tx *gorm.DB, customer *system.Customer, positions []system.TradePosition, closedAt int64, reason string) error {
	for index := range positions {
		position := &positions[index]
		hk := strings.HasSuffix(position.Symbol, ".HK")
		price := position.CurrentPrice
		var security system.StockSecurity
		if !hk && tx.Where("symbol = ?", position.Symbol).First(&security).Error == nil && security.LastPrice > 0 {
			price = security.LastPrice
		}
		amount := roundMoney(price * position.PositionQty)
		realized := roundMoney((amount - position.TotalCost) * positionbasis.Multiplier(*position))
		if hk {
			if position.Currency != "HKD" {
				return fmt.Errorf("港股强平币种异常")
			}
			settlement := roundMoney(position.Margin + realized)
			customer.HKDBalance = roundMoney(customer.HKDBalance + settlement)
			direction := "入账"
			if settlement < 0 {
				direction = "扣款"
			}
			if err := tx.Create(&system.CustomerFundRecord{CustomerID: customer.ID, Type: "港股风险强平", Direction: direction, Currency: "HKD", Amount: math.Abs(settlement), Balance: customer.HKDBalance, Remark: reason + "；" + position.Symbol}).Error; err != nil {
				return err
			}
		} else {
			customer.Balance = roundMoney(customer.Balance + position.Margin + realized)
		}
		if !hk && realized >= 0 {
			customer.TotalProfit = roundMoney(customer.TotalProfit + realized)
		} else if !hk {
			customer.TotalLoss = roundMoney(customer.TotalLoss - realized)
		}
		record := system.TradeRecord{QuantityBasis: position.QuantityBasis, Leverage: positionLeverage(*position), CustomerID: customer.ID, Symbol: position.Symbol, StockName: position.StockName, Currency: position.Currency, Direction: "卖出", TradePrice: price, Quantity: position.PositionQty, Amount: amount, Remark: reason, TradeAt: closedAt}
		cost := position.TotalCost
		record.RealizedProfit, record.RealizedCost = &realized, &cost
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		position.PositionQty, position.AvailableQty = 0, 0
		position.TotalCost, position.Margin, position.MarketValue = 0, 0, 0
		positionbasis.SetSupplementalMargin(position, 0)
		position.CostPrice, position.ProfitLoss, position.ProfitRate = 0, 0, 0
		position.CurrentPrice, position.Status = price, system.StatusDisabled
		if err := tx.Save(position).Error; err != nil {
			return err
		}
	}
	return nil
}
