package mobile

import (
	"encoding/json"
	"math"
	"strings"
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"api-server/util/positionquote"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestHoldingViewExcludesValuation(t *testing.T) {
	item := system.TradePosition{Symbol: "00875.HK", Currency: "HKD", StockName: "测试港股",
		PositionQty: 1000, AvailableQty: 600, Leverage: 1, CurrentPrice: 999, MarketValue: 999000, ProfitLoss: 998000}
	view := holdingView(item)
	if view.Symbol != item.Symbol || view.Currency != "HKD" || view.PositionQty != 1000 || view.AvailableQty != 600 || view.Leverage != 1 {
		t.Fatalf("基础持仓应保留真实数量及冻结后的可卖量：%+v", view)
	}
	body, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(body, &fields); err != nil {
		t.Fatal(err)
	}
	if len(fields) != 8 {
		t.Fatalf("基础持仓只返回八个账本字段（含数量口径）：%s", body)
	}
	for _, key := range []string{"current_price", "market_value", "profit_loss", "quote_at", "cost_price", "margin"} {
		if _, exists := fields[key]; exists {
			t.Fatalf("基础持仓不得返回未校验估值：%s", body)
		}
	}
}

func TestCustomerPositionQueryIsolation(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=127.0.0.1 user=test dbname=test sslmode=disable"),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, market := range []string{"A", "HK"} {
		var items []system.TradePosition
		query := customerPositionQuery(db, 42, market).Find(&items)
		sql := query.Statement.SQL.String()
		if query.Error != nil || !strings.Contains(sql, "customer_id =") || !strings.Contains(sql, "position_qty > 0") ||
			!strings.Contains(sql, "deleted_at") || !strings.Contains(sql, "id DESC") {
			t.Fatalf("基础持仓及估值必须共用当前客户、未平仓及软删除过滤：%s，%v", sql, query.Error)
		}
		if (market == "A") != strings.Contains(sql, "NOT LIKE") {
			t.Fatalf("两市场查询必须隔离：%s", sql)
		}
		if len(query.Statement.Vars) != 2 || query.Statement.Vars[0] != "%.HK" || query.Statement.Vars[1] != uint(42) {
			t.Fatalf("查询参数错误：%v", query.Statement.Vars)
		}
	}
}

func TestAStockTradeQuoteTime(t *testing.T) {
	location := time.FixedZone("UTC+8", 8*3600)
	at := func(value string) time.Time {
		result, err := time.ParseInLocation("20060102150405", value, location)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	now := at("20261023133200")
	for _, tc := range []struct {
		name, quoted string
		now          time.Time
		price        float64
		limit, valid bool
	}{
		{"新报价", "20261023133100", now, 112.3, false, true},
		{"30分钟边界", "20261023130200", now, 112.3, false, true},
		{"超过30分钟", "20261023130159", now, 112.3, false, false},
		{"上午旧报价", "20261023104646", now, 112.3, false, false},
		{"时间缺失", "", now, 112.3, false, false},
		{"未来报价", "20261023133301", now, 112.3, false, false},
		{"无效价格", "20261023133100", now, math.Inf(1), false, false},
		{"午间收盘报价", "20261023112900", at("20261023123000"), 112.3, false, true},
		{"盘外最近收市报价", "20261022145900", at("20261023080000"), 112.3, false, true},
		{"限价不能用盘外旧盘口", "20261022145900", at("20261023080000"), 112.3, true, false},
		{"限价新盘口", "20261023133100", now, 112.3, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateAStockTradeQuote(orderBookView{LastPrice: tc.price, UpdatedAt: tc.quoted}, tc.now, tc.limit)
			if (err == nil) != tc.valid {
				t.Fatalf("有效=%v，错误=%v", tc.valid, err)
			}
		})
	}
}

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
