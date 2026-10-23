package mobile

import (
	"testing"

	"api-server/db/pgdb/system"
	"api-server/util/positionquote"
)

func TestValueCustomerPositionDoesNotUseOldPrice(t *testing.T) {
	item := system.TradePosition{Symbol: "002384.SZ", Currency: "CNY", PositionQty: 700, AvailableQty: 700,
		CostPrice: 106.24, TotalCost: 74368, Margin: 74368, Leverage: 5, CurrentPrice: 123.17,
		MarketValue: 431095, ProfitLoss: 59255}
	for _, tc := range []struct {
		price, profit, value float64
	}{
		{106.24, 0, 371840},
		{106.15, -315, 371525},
		{106.33, 315, 372155},
	} {
		before := item
		view := valueCustomerPosition(item, positionquote.Quote{Price: tc.price, At: 1792725540})
		if view.CurrentPrice != tc.price || view.ProfitLoss != tc.profit || view.MarketValue != tc.value ||
			view.QuoteAt != 1792725540 || view.Margin != 74368 || view.CostPrice != 106.24 || view.PositionQty != 700 {
			t.Fatalf("持仓估值不应使用123.17旧价或修改成本：%+v", view)
		}
		if item != before {
			t.Fatal("只读估值不得修改持仓原始记录")
		}
	}
}

func TestValueCustomerPositionHK(t *testing.T) {
	item := system.TradePosition{Symbol: "09889.HK", Currency: "HKD", PositionQty: 1000, AvailableQty: 1000,
		CostPrice: 3.15, TotalCost: 3150, Margin: 3150, Leverage: 1}
	item.CurrentPrice, item.MarketValue, item.ProfitLoss = 123.17, 123170, 120020
	for _, tc := range []struct{ price, profit, value float64 }{{3.2, 50, 3200}, {3.1, -50, 3100}, {3.15, 0, 3150}} {
		before := item
		view := valueCustomerPosition(item, positionquote.Quote{Price: tc.price, At: 1792725540, Source: "eastmoney"})
		if view.ProfitLoss != tc.profit || view.MarketValue != tc.value || view.CurrentPrice != tc.price || view.Currency != "HKD" || view.Leverage != 1 || view.QuoteSource != "eastmoney" || view.QuoteAt != 1792725540 {
			t.Fatalf("港股应按新报价进行独立港币全额估值：%+v", view)
		}
		if item != before {
			t.Fatal("港股只读估值不得改变成本、持仓或历史盈亏")
		}
	}
}
