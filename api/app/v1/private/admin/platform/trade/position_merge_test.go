package trade

import (
	"math"
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"api-server/util/positionbasis"
	"api-server/util/positionsettlement"
	"gorm.io/gorm"
)

func TestMergeManualPositionsUsesLatestPendingSettlement(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, positionsettlement.Location)
	monday := time.Date(2026, 11, 23, 0, 0, 0, 0, positionsettlement.Location).Unix()
	tuesday := time.Date(2026, 11, 24, 0, 0, 0, 0, positionsettlement.Location).Unix()
	firstBuyAt := time.Date(2026, 11, 1, 10, 0, 0, 0, positionsettlement.Location).Unix()
	secondBuyAt := time.Date(2026, 11, 5, 10, 0, 0, 0, positionsettlement.Location).Unix()
	newBuyAt := now.Unix()
	firstSupplemental := 25.0
	newSupplemental := 0.0

	existing := []system.TradePosition{
		{
			Model:      gorm.Model{ID: 10},
			CustomerID: 7, Symbol: "000001.SZ", Currency: "CNY", QuantityBasis: positionbasis.Actual,
			PositionQty: 100, AvailableQty: 80, UnsettledQty: 20, SellableAt: monday,
			SettlementVerified: true, CostPrice: 10, TotalCost: 1000, Margin: 200,
			SupplementalMargin: &firstSupplemental, Leverage: 5, MarginCallLevel: 9,
			Status: system.StatusEnabled, BuyAt: firstBuyAt,
		},
		{
			Model:      gorm.Model{ID: 11},
			CustomerID: 7, Symbol: "000001.SZ", Currency: "CNY", QuantityBasis: positionbasis.Actual,
			PositionQty: 50, AvailableQty: 40, UnsettledQty: 10, SellableAt: tuesday,
			SettlementVerified: true, CostPrice: 20, TotalCost: 1000, Margin: 200,
			Leverage: 5, MarginCallLevel: 18, Status: system.StatusEnabled, BuyAt: secondBuyAt,
		},
	}
	incoming := system.TradePosition{
		CustomerID: 7, Symbol: "000001.SZ", Currency: "CNY", QuantityBasis: positionbasis.Actual,
		PositionQty: 25, AvailableQty: 0, UnsettledQty: 25, SellableAt: monday,
		SettlementVerified: true, CostPrice: 30, TotalCost: 750, Margin: 150,
		SupplementalMargin: &newSupplemental, Leverage: 5, CurrentPrice: 20,
		Status: system.StatusEnabled, BuyAt: newBuyAt,
	}

	merged, duplicateIDs, shouldMerge, err := mergeManualPositions(existing, incoming, now)
	if err != nil || !shouldMerge {
		t.Fatalf("应成功合并同类持仓：%v", err)
	}
	if merged.ID != 10 || merged.PositionQty != 175 || merged.AvailableQty != 120 ||
		merged.UnsettledQty != 55 || merged.SellableAt != tuesday || !merged.SettlementVerified ||
		merged.BuyAt != firstBuyAt || merged.MarginCallLevel != 18 ||
		len(duplicateIDs) != 1 || duplicateIDs[0] != 11 {
		t.Fatalf("合并后的持仓数量或T+1状态错误：%+v，重复ID=%v", merged, duplicateIDs)
	}
	near := func(actual, expected float64) {
		t.Helper()
		if math.Abs(actual-expected) > 0.000001 {
			t.Fatalf("实际=%v，预期=%v", actual, expected)
		}
	}
	near(merged.TotalCost, 2750)
	near(merged.CostPrice, 2750.0/175)
	near(merged.Margin, 550)
	near(*merged.SupplementalMargin, 25)
	near(merged.MarketValue, 3500)
	near(merged.ProfitLoss, 750)
	near(merged.ProfitRate, 27.27)
}

func TestMergeManualPositionsKeepsUnverifiedHoldingsLocked(t *testing.T) {
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, positionsettlement.Location)
	existing := []system.TradePosition{{
		Model:      gorm.Model{ID: 10},
		CustomerID: 7, Symbol: "000001.SZ", Currency: "CNY", QuantityBasis: positionbasis.Actual,
		PositionQty: 100, AvailableQty: 100, CostPrice: 10, TotalCost: 1000, Margin: 200,
		Leverage: 5, Status: system.StatusEnabled, SettlementVerified: false,
	}}
	incoming := system.TradePosition{
		CustomerID: 7, Symbol: "000001.SZ", Currency: "CNY", QuantityBasis: positionbasis.Actual,
		PositionQty: 50, CostPrice: 10, TotalCost: 500, Margin: 100, Leverage: 5,
		AvailableQty: 0, UnsettledQty: 50, SellableAt: now.AddDate(0, 0, 3).Unix(),
		SettlementVerified: true, Status: system.StatusEnabled,
	}

	merged, _, shouldMerge, err := mergeManualPositions(existing, incoming, now)
	if err != nil || !shouldMerge {
		t.Fatalf("应能合并但继续锁定未核对持仓：%v", err)
	}
	if merged.PositionQty != 150 || merged.SettlementVerified || merged.AvailableQty != 0 ||
		merged.UnsettledQty != 0 || merged.SellableAt != 0 {
		t.Fatalf("未核对的合并持仓不应变为可交易：%+v", merged)
	}
}

func TestMergeManualPositionsRejectsDifferentLeverage(t *testing.T) {
	existing := []system.TradePosition{{
		Model:      gorm.Model{ID: 10},
		CustomerID: 7, Symbol: "000001.SZ", Currency: "CNY", QuantityBasis: positionbasis.Actual,
		PositionQty: 100, AvailableQty: 100, CostPrice: 10, TotalCost: 1000, Margin: 1000,
		Leverage: 1, Status: system.StatusEnabled, SettlementVerified: true,
	}}
	incoming := system.TradePosition{
		CustomerID: 7, Symbol: "000001.SZ", Currency: "CNY", QuantityBasis: positionbasis.Actual,
		PositionQty: 50, CostPrice: 10, TotalCost: 500, Margin: 100, Leverage: 5,
		Status: system.StatusEnabled, SettlementVerified: true,
	}

	_, duplicateIDs, shouldMerge, err := mergeManualPositions(existing, incoming, time.Now())
	if err == nil || shouldMerge || len(duplicateIDs) != 0 {
		t.Fatalf("不同孖展倍数的持仓应拒绝新建以避免重复持仓：合并=%v，err=%v", shouldMerge, err)
	}
}
