package mobile

import (
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"api-server/util/positionbasis"
	"api-server/util/positionquote"
)

func TestHongKongRiskReleasesOwnOrdersBeforeSupplement(t *testing.T) {
	db := financingTestDB(t)
	now := time.Now()
	c := system.Customer{Phone: "hk-risk-release", Balance: 50000, FrozenBalance: 1234, HKDBalance: 0, HKDFrozenBalance: 2000, Status: 1, FundStatus: 1, Verified: 1}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	p := system.TradePosition{CustomerID: c.ID, Symbol: "02348.HK", Currency: "HKD", PositionQty: 1000, AvailableQty: 0, CostPrice: 10, TotalCost: 10000, Margin: 2000, Leverage: 5, Status: 1}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	orders := []system.LimitOrder{
		{CustomerID: c.ID, Symbol: p.Symbol, Direction: "买入", Quantity: 1000, FrozenAmount: 2000, LimitPrice: 10, Status: limitOrderPending, Leverage: 5, QuantityBasis: 1},
		{CustomerID: c.ID, Symbol: p.Symbol, Direction: "卖出", Quantity: 1000, FrozenQuantity: 1000, LimitPrice: 11, Status: limitOrderPending, Leverage: 5, QuantityBasis: 1},
		{CustomerID: c.ID, Symbol: "000001.SZ", Direction: "买入", Quantity: 100, FrozenAmount: 1234, LimitPrice: 10, Status: limitOrderPending, Leverage: 5, QuantityBasis: 1},
	}
	if err := db.Create(&orders).Error; err != nil {
		t.Fatal(err)
	}
	quotes := map[string]positionquote.Quote{p.Symbol: {Price: 8.4, At: now.Unix()}}
	n, forced, err := processCustomerMarginCallsWithQuotesDB(db, c.ID, 16, .01, now, "HK", quotes)
	if err != nil || forced || n != 1 {
		t.Fatal("应先只撤港股挂单，再使用释放的港币补仓", n, forced, err)
	}
	if err := db.First(&c, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&p, p.ID).Error; err != nil {
		t.Fatal(err)
	}
	base, extra := positionbasis.MarginParts(p)
	if c.Balance != 50000 || c.FrozenBalance != 1234 || c.HKDBalance != 1916 || c.HKDFrozenBalance != 0 ||
		p.AvailableQty != 1000 || p.Margin != 2084 || base != 2000 || extra != 84 {
		t.Fatal("撤单/解冻/补仓不得串币种或丢追加担保", c.HKDBalance, p.Margin, base, extra)
	}
	for i := range orders {
		if err := db.First(&orders[i], orders[i].ID).Error; err != nil {
			t.Fatal(err)
		}
		want := limitOrderCancelled
		if i == 2 {
			want = limitOrderPending
		}
		if orders[i].Status != want {
			t.Fatal("只能取消港股挂单", orders[i])
		}
	}
	if n, forced, err := processCustomerMarginCallsWithQuotesDB(db, c.ID, 16, .01, now, "HK", quotes); err != nil || forced || n != 0 {
		t.Fatal("不得重复解冻或重复扣补仓", n, forced, err)
	}
}
