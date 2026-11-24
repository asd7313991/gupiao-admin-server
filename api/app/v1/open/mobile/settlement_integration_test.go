package mobile

import (
	"errors"
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"api-server/util/positionsettlement"
	"gorm.io/gorm"
)

func TestSettlementHoldingsView(t *testing.T) {
	old := system.TradePosition{Symbol: "600103.SH", PositionQty: 1000, AvailableQty: 1000}
	if view := holdingView(old); view.AvailableQty != 0 || view.SettlementNotice == "" {
		t.Fatal("旧持仓不能继续返回未经核对的可卖数量", view)
	}
	old.SettlementVerified, old.AvailableQty, old.UnsettledQty = true, 600, 300
	old.SellableAt = time.Now().Add(-time.Hour).Unix()
	if view := holdingView(old); view.AvailableQty != 900 || view.SettlementNotice != "" {
		t.Fatal("接口应投影交收后的可卖数，保留100挂单冻结股", view)
	}
	if old.AvailableQty != 600 || old.UnsettledQty != 300 {
		t.Fatal("只读投影不能修改传入持仓")
	}
}

func TestAStockSettlementOrderLifecycle(t *testing.T) {
	db := financingTestDB(t)
	now := time.Date(2026, 11, 20, 10, 0, 0, 0, positionsettlement.Location)
	customer := system.Customer{Phone: "settlement-test", Balance: 50000, Status: 1, FundStatus: 1, Verified: 1}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	security := system.StockSecurity{Code: "600103", Symbol: "600103.SH", Name: "交收测试", LastPrice: 10, Status: 1}
	firstBuyAt := now.AddDate(0, 0, -2).Unix()
	position := system.TradePosition{CustomerID: customer.ID, Symbol: security.Symbol, Currency: "CNY",
		PositionQty: 1000, AvailableQty: 1000, BuyAt: firstBuyAt, TotalCost: 10000,
		CostPrice: 10, Margin: 2000, Leverage: 5, QuantityBasis: 1, Status: 1}
	if err := db.Create(&position).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Create(&[]system.TradeRecord{
		{CustomerID: customer.ID, Symbol: security.Symbol, Currency: "CNY", Direction: "买入", Quantity: 600, TradeAt: firstBuyAt},
		{CustomerID: customer.ID, Symbol: security.Symbol, Currency: "CNY", Direction: "买入", Quantity: 400, TradeAt: now.AddDate(0, 0, -1).Unix()},
	}).Error; err != nil {
		t.Fatal(err)
	}
	displayPositions := []system.TradePosition{position}
	if err := reconcilePositionViews(db, displayPositions, now); err != nil ||
		!displayPositions[0].SettlementVerified ||
		displayPositions[0].AvailableQty != 1000 || displayPositions[0].UnsettledQty != 0 {
		t.Fatal("完整买入成交记录应还原分批买入且全部已交收的旧持仓", displayPositions[0], err)
	}
	var storedPosition system.TradePosition
	if err := db.First(&storedPosition, position.ID).Error; err != nil ||
		storedPosition.SettlementVerified || storedPosition.AvailableQty != position.AvailableQty {
		t.Fatal("只读持仓展示不能持久化自动核对结果", storedPosition, err)
	}
	settings := mobileTradeSettings{}
	settings.Trade.AllDay = true
	run := func(direction string, quantity float64, at time.Time, orders ...system.LimitOrder) error {
		t.Helper()
		var result orderResult
		var rejection string
		return db.Transaction(func(tx *gorm.DB) error {
			if err := tx.First(&customer, customer.ID).Error; err != nil {
				return err
			}
			return executeMobileOrderAt(tx, &customer, security, orderInput{Direction: direction, Quantity: quantity},
				settings, &result, &rejection, at, orders...)
		})
	}
	// 限价买单按实际成交日交收，不按下单日期提前解锁。
	if err := run("买入", 300, now, system.LimitOrder{QuantityBasis: 1, Leverage: 5}); err != nil {
		t.Fatal(err)
	}
	if err := run("卖出", 1000, now); err != nil {
		t.Fatal("今天加仓不能锁住已有1000股", err)
	}
	if err := run("卖出", 300, now); !errors.Is(err, errTradeRejected) {
		t.Fatal("全天模拟也不能卖出当天新增300股", err)
	}
	if _, _, err := placeAStockLimitOrderDBAt(db, customer.ID, security,
		limitOrderInput{Direction: "卖出", Quantity: 300, LimitPrice: 11}, settings, now); !errors.Is(err, errTradeRejected) {
		t.Fatal("不能通过卖出挂单绕过T+1", err)
	}
	if err := run("卖出", 300, now.AddDate(0, 0, 1)); !errors.Is(err, errTradeRejected) {
		t.Fatal("周六不能解锁周五买入", err)
	}
	monday := now.AddDate(0, 0, 3)
	order, rejection, err := placeAStockLimitOrderDBAt(db, customer.ID, security,
		limitOrderInput{Direction: "卖出", Quantity: 100, LimitPrice: 11}, settings, monday)
	if err != nil {
		t.Fatal(rejection, err)
	}
	if err := db.First(&position, position.ID).Error; err != nil {
		t.Fatal(err)
	}
	if position.AvailableQty != 200 || position.UnsettledQty != 0 || order.FrozenQuantity != 100 {
		t.Fatal("周一只解锁一次，挂单冻结100股", position, order)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return releaseAStockPositionFreeze(tx, order) }); err != nil {
		t.Fatal(err)
	}
	if err := run("卖出", 300, monday); err != nil {
		t.Fatal("周一撤单后可以卖出全部300股", err)
	}
}
