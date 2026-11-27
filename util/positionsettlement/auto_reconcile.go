package positionsettlement

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"time"

	"api-server/db/pgdb/system"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

type positionGroupKey struct {
	customerID uint
	symbol     string
}

func AutoReconcileLegacyPositions(db *gorm.DB, positions []system.TradePosition, now time.Time) error {
	groups := make(map[positionGroupKey]struct{})
	order := make([]positionGroupKey, 0)
	for _, position := range positions {
		if HongKong(position) || position.SettlementVerified {
			continue
		}
		key := positionGroupKey{customerID: position.CustomerID, symbol: position.Symbol}
		if _, exists := groups[key]; !exists {
			groups[key] = struct{}{}
			order = append(order, key)
		}
	}

	for _, key := range order {
		var current []system.TradePosition
		err := db.Transaction(func(tx *gorm.DB) error {
			var err error
			current, err = reconcileLegacyPositionGroup(tx, key, now)
			return err
		})
		if err != nil {
			return err
		}
		updatedByID := make(map[uint]system.TradePosition, len(current))
		for _, position := range current {
			updatedByID[position.ID] = position
		}
		for index := range positions {
			if position, exists := updatedByID[positions[index].ID]; exists {
				positions[index] = position
			}
		}
	}
	return nil
}

func reconcileLegacyPositionGroup(tx *gorm.DB, key positionGroupKey, now time.Time) ([]system.TradePosition, error) {
	var positions []system.TradePosition
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).
		Where("customer_id = ? AND symbol = ? AND position_qty > 0", key.customerID, key.symbol).
		Order("id ASC").Find(&positions).Error; err != nil {
		return nil, err
	}
	if len(positions) == 0 {
		return positions, nil
	}

	frozen, err := pendingSellFrozen(tx, key.customerID, key.symbol)
	if err != nil {
		return nil, err
	}
	var records []system.TradeRecord
	if len(positions) == 1 {
		if err := tx.Where("customer_id = ? AND symbol = ?", key.customerID, key.symbol).
			Order("trade_at ASC, id ASC").Find(&records).Error; err != nil {
			return nil, fmt.Errorf("读取持仓成交记录失败: %w", err)
		}
	}

	alreadyReserved := float64(0)
	for _, position := range positions {
		if !position.SettlementVerified {
			continue
		}
		gap := position.PositionQty - position.AvailableQty - position.UnsettledQty
		if validQuantity(gap) {
			alreadyReserved += gap
		}
	}
	frozenToAllocate := math.Max(0, frozen-alreadyReserved)

	for index := range positions {
		position := &positions[index]
		if position.SettlementVerified {
			continue
		}
		before, err := json.Marshal(position)
		if err != nil {
			return nil, err
		}

		fallback := len(positions) > 1
		if !fallback {
			err = ReconcileLegacyWithFrozen(position, records, now, frozen)
			fallback = errors.Is(err, ErrReview)
		}
		if fallback {
			err = ReconcileLegacyFromBuyAt(position, now, 0)
			if err == nil {
				reserved := math.Min(position.AvailableQty, frozenToAllocate)
				position.AvailableQty -= reserved
				frozenToAllocate -= reserved
				position.SettlementNotice = "历史成交记录不完整，已按持仓买入时间估算T+1交收"
			}
		}
		if err != nil {
			if errors.Is(err, ErrReview) {
				continue
			}
			return nil, err
		}

		after, err := json.Marshal(position)
		if err != nil {
			return nil, err
		}
		if err := tx.Save(position).Error; err != nil {
			return nil, err
		}
		if err := tx.Create(&system.PositionSettlementReview{
			PositionID: position.ID,
			Before:     string(before),
			After:      string(after),
		}).Error; err != nil {
			return nil, err
		}
	}
	return positions, nil
}

func AutoReconcileLegacyPosition(db *gorm.DB, position *system.TradePosition, now time.Time) error {
	if position.ID == 0 {
		return ErrReview
	}
	if HongKong(*position) || position.SettlementVerified {
		return nil
	}
	return db.Transaction(func(tx *gorm.DB) error {
		var current system.TradePosition
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, position.ID).Error; err != nil {
			return err
		}
		err := AutoReconcileLegacyInTransaction(tx, &current, now)
		*position = current
		return err
	})
}

func AutoReconcileLegacyInTransaction(tx *gorm.DB, position *system.TradePosition, now time.Time, excludedOrderIDs ...uint) error {
	if HongKong(*position) || position.SettlementVerified {
		return nil
	}

	var openPositions int64
	if err := tx.Model(&system.TradePosition{}).
		Where("customer_id = ? AND symbol = ? AND position_qty > 0", position.CustomerID, position.Symbol).
		Count(&openPositions).Error; err != nil {
		return err
	}
	if openPositions != 1 {
		return ErrReview
	}

	frozen, err := pendingSellFrozen(tx, position.CustomerID, position.Symbol, excludedOrderIDs...)
	if err != nil {
		return err
	}
	var records []system.TradeRecord
	if err := tx.Where("customer_id = ? AND symbol = ?", position.CustomerID, position.Symbol).
		Order("trade_at ASC, id ASC").Find(&records).Error; err != nil {
		return fmt.Errorf("读取持仓成交记录失败: %w", err)
	}

	before, err := json.Marshal(position)
	if err != nil {
		return err
	}
	err = ReconcileLegacyWithFrozen(position, records, now, frozen)
	if errors.Is(err, ErrReview) {
		err = ReconcileLegacyFromBuyAt(position, now, frozen)
	}
	if err != nil {
		return err
	}
	after, err := json.Marshal(position)
	if err != nil {
		return err
	}
	if err := tx.Save(position).Error; err != nil {
		return err
	}
	return tx.Create(&system.PositionSettlementReview{
		PositionID: position.ID,
		Before:     string(before),
		After:      string(after),
	}).Error
}

func pendingSellFrozen(tx *gorm.DB, customerID uint, symbol string, excludedOrderIDs ...uint) (float64, error) {
	query := tx.Model(&system.LimitOrder{}).
		Select("COALESCE(SUM(CASE WHEN frozen_quantity > 0 THEN frozen_quantity ELSE quantity END), 0)").
		Where("customer_id = ? AND symbol = ? AND direction = ? AND status = ?",
			customerID, symbol, "卖出", "pending")
	for _, id := range excludedOrderIDs {
		if id != 0 {
			query = query.Where("id <> ?", id)
		}
	}
	var frozen float64
	if err := query.Scan(&frozen).Error; err != nil {
		return 0, err
	}
	return frozen, nil
}
