package mobile

import (
	"fmt"
	"os"
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"api-server/util/positionbasis"
	"api-server/util/positionquote"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func financingTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("POSITION_BASIS_TEST_DSN")
	if dsn == "" {
		t.Skip("未配置独立融资测试数据库")
	}
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	rootSQL, err := root.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { rootSQL.Close() })
	schema := fmt.Sprintf("financing_%d", time.Now().UnixNano())
	if err := root.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := root.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	})
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sqlDB.Close() })
	if err := db.AutoMigrate(&system.Customer{}, &system.StockSecurity{}, &system.TradePosition{}, &system.TradeRecord{},
		&system.CustomerFundRecord{}, &system.LimitOrder{}, &system.AppSystemSetting{}, &system.HongKongTradeRequest{}); err != nil {
		t.Fatal(err)
	}
	return db
}

func TestFinancingMarginLifecycle(t *testing.T) {
	for _, market := range []string{"A", "HK"} {
		t.Run(market, func(t *testing.T) {
			db := financingTestDB(t)
			now := time.Date(2026, 11, 2, 11, 0, 0, 0, time.FixedZone("HK", 8*3600))
			customer := system.Customer{Phone: "financing-lifecycle", Balance: 10000, HKDBalance: 10000, Status: 1, FundStatus: 1, Verified: 1}
			stock := system.StockSecurity{Code: "000001", Symbol: "000001.SZ", Name: "融资测试", LastPrice: 10, Status: 1}
			if market == "HK" {
				stock.Code, stock.Symbol, stock.Market = "02348", "02348.HK", "港股"
			}
			setting := system.AppSystemSetting{Config: `{"trade":{"allDay":true},"hkTrade":{"enabled":true,"commission":0,"minCommission":0,"stampDuty":0,"regulatoryFee":0,"tradingFee":0,"settlementFee":0}}`}
			for _, value := range []any{&customer, &stock, &setting} {
				if err := db.Create(value).Error; err != nil {
					t.Fatal(err)
				}
			}
			sequence := 0
			run := func(direction string, qty, price float64) error {
				sequence++
				return db.Transaction(func(tx *gorm.DB) error {
					if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, customer.ID).Error; err != nil {
						return err
					}
					stock.LastPrice = price
					if market == "HK" {
						_, err := executeHongKongOrder(tx, &customer, stock,
							hongKongQuote{Code: stock.Code, LastPrice: price, LotSize: 100, Kind: "GP", Currency: "HKD", QuoteAt: now.Unix()},
							hongKongOrderInput{orderInput: orderInput{Code: stock.Code, Direction: direction, Quantity: qty}, RequestID: fmt.Sprintf("lifecycle-%d", sequence)}, now, false)
						return err
					}
					var result orderResult
					var rejection string
					return executeMobileOrder(tx, &customer, stock, orderInput{Direction: direction, Quantity: qty},
						mobileTradeSettings{}, &result, &rejection)
				})
			}
			check := func(qty, cost, total, base, extra, balance float64) system.TradePosition {
				t.Helper()
				var p system.TradePosition
				if err := db.Where("customer_id = ? AND symbol = ?", customer.ID, stock.Symbol).First(&p).Error; err != nil {
					t.Fatal(err)
				}
				if err := db.First(&customer, customer.ID).Error; err != nil {
					t.Fatal(err)
				}
				actualBalance, other := customer.Balance, customer.HKDBalance
				if market == "HK" {
					actualBalance, other = customer.HKDBalance, customer.Balance
				}
				b, e := positionbasis.MarginParts(p)
				if p.PositionQty != qty || p.TotalCost != cost || p.Margin != total || b != base || e != extra ||
					p.SupplementalMargin == nil || actualBalance != balance || other != 10000 || p.Leverage != 5 {
					t.Fatalf("资金或担保金错误：position=%+v base=%v extra=%v balance=%v other=%v", p, b, e, actualBalance, other)
				}
				return p
			}
			if err := run("买入", 200, 10); err != nil {
				t.Fatal(err)
			}
			check(200, 2000, 400, 400, 0, 9600)
			if err := run("买入", 100, 10); err != nil {
				t.Fatal(err)
			}
			check(300, 3000, 600, 600, 0, 9400)
			if err := db.Model(&stock).Update("last_price", 8.4).Error; err != nil {
				t.Fatal(err)
			}
			quotes := map[string]positionquote.Quote{stock.Symbol: {Price: 8.4, At: now.Unix()}}
			for attempt := 0; attempt < 2; attempt++ {
				n, forced, err := processCustomerMarginCallsWithQuotesDB(db, customer.ID, 16, .01, now, market, quotes)
				if err != nil || forced || n != 1-attempt {
					t.Fatalf("补仓应仅实际扣一次：n=%d forced=%v err=%v", n, forced, err)
				}
			}
			check(300, 3000, 625.2, 600, 25.2, 9374.8)
			if err := run("卖出", 100, 11); err != nil {
				t.Fatal(err)
			}
			check(200, 2000, 416.8, 400, 16.8, 9683.2)
			if err := run("卖出", 200, 9); err != nil {
				t.Fatal(err)
			}
			check(0, 0, 0, 0, 0, 9900)
			if err := run("买入", 100, 10); err != nil {
				t.Fatal(err)
			}
			check(100, 1000, 200, 200, 0, 9700)
		})
	}
}

func TestHongKongLegacyPendingFinancingCompatibility(t *testing.T) {
	db := financingTestDB(t)
	now := time.Date(2026, 11, 2, 11, 0, 0, 0, time.FixedZone("HK", 8*3600))
	c := system.Customer{Phone: "legacy-hk-orders", Balance: 10000, HKDBalance: 10000, HKDFrozenBalance: 1010, Status: 1, FundStatus: 1, Verified: 1}
	s := system.StockSecurity{Code: "02348", Symbol: "02348.HK", Market: "港股", Name: "旧挂单测试", Status: 1}
	setting := system.AppSystemSetting{Config: `{"trade":{"allDay":true},"hkTrade":{"enabled":true,"commission":0,"stampDuty":0,"regulatoryFee":0,"tradingFee":0,"settlementFee":0}}`}
	for _, value := range []any{&c, &s, &setting} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	old := system.LimitOrder{CustomerID: c.ID, Symbol: s.Symbol, StockName: s.Name, Direction: "买入", Quantity: 100,
		LimitPrice: 10.1, Status: limitOrderPending, FrozenAmount: 1010}
	if err := db.Create(&old).Error; err != nil {
		t.Fatal(err)
	}
	q := hongKongQuote{Code: s.Code, LastPrice: 10, LotSize: 100, Currency: "HKD", Kind: "GP", QuoteAt: now.Unix(),
		Ask: []orderBookLevel{{Price: 10, Volume: 100}}, Bid: []orderBookLevel{{Price: 10, Volume: 100}}}
	if filled, err := matchHongKongLimitOrderDB(db, c.ID, old.ID, q, now); err != nil || !filled {
		t.Fatalf("旧全额冻结款须按1倍原规则成交：filled=%v err=%v", filled, err)
	}
	var p system.TradePosition
	if err := db.Where("customer_id = ?", c.ID).First(&p).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&c, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	if p.Leverage != 1 || p.Margin != 1000 || c.HKDBalance != 10010 || c.HKDFrozenBalance != 0 || c.Balance != 10000 {
		t.Fatal("不能把旧全额挂单静默改为5倍，或多退/扣资金", p, c.HKDBalance)
	}
	input := hongKongLimitInput{limitOrderInput: limitOrderInput{Code: s.Code, Direction: "买入", Quantity: 100, LimitPrice: 10}, RequestID: "legacy-new-buy-rejected"}
	if _, err := placeHongKongLimitOrderDB(db, c.ID, s, q, input, now); err == nil {
		t.Fatal("旧1倍仓未转换前不能新增5倍加仓单")
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		_, err := executeHongKongOrder(tx, &c, s, q,
			hongKongOrderInput{orderInput: orderInput{Code: s.Code, Direction: "买入", Quantity: 100}, RequestID: "legacy-market-buy-rejected"}, now, false)
		return err
	}); err == nil {
		t.Fatal("旧1倍仓未转换前不能市价混入5倍本金")
	}
	input.Direction, input.LimitPrice, input.RequestID = "卖出", 11, "legacy-sell-snapshot"
	q.LastPrice = 11
	q.Ask, q.Bid = []orderBookLevel{{Price: 11, Volume: 100}}, []orderBookLevel{{Price: 11, Volume: 100}}
	sale, err := placeHongKongLimitOrderDB(db, c.ID, s, q, input, now)
	if err != nil || sale.Leverage != 1 {
		t.Fatal("旧仓仍须可按原1倍卖出", sale, err)
	}
	if filled, err := matchHongKongLimitOrderDB(db, c.ID, sale.ID, q, now); err != nil || !filled {
		t.Fatal("旧1倍卖出撮合失败", filled, err)
	}
	if err := db.First(&c, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	if c.HKDBalance != 11110 || c.HKDFrozenBalance != 0 {
		t.Fatalf("旧仓须返本金1000加盈利100：%v", c.HKDBalance)
	}
}

func TestHongKongFinancingRiskIsolationAndDebt(t *testing.T) {
	db := financingTestDB(t)
	now := time.Now()
	c := system.Customer{Phone: "hk-risk-isolation", Balance: 50000, HKDBalance: 0, Status: 1, FundStatus: 1, Verified: 1}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	positions := []system.TradePosition{
		{CustomerID: c.ID, Symbol: "02348.HK", Currency: "HKD", PositionQty: 1000, AvailableQty: 1000, CostPrice: 10, TotalCost: 10000, Margin: 2000, Leverage: 5, Status: 1},
		{CustomerID: c.ID, Symbol: "00875.HK", Currency: "HKD", PositionQty: 100, AvailableQty: 100, CostPrice: 10, TotalCost: 1000, Margin: 1000, Leverage: 1, Status: 1},
		{CustomerID: c.ID, Symbol: "000001.SZ", Currency: "CNY", PositionQty: 1000, AvailableQty: 1000, CostPrice: 10, TotalCost: 10000, Margin: 2000, Leverage: 5, QuantityBasis: 1, Status: 1},
	}
	if err := db.Create(&positions).Error; err != nil {
		t.Fatal(err)
	}
	stale := system.StockSecurity{Code: "02348", Symbol: "02348.HK", LastPrice: 1, Status: 1}
	if err := db.Create(&stale).Error; err != nil {
		t.Fatal(err)
	}
	if _, forced, err := processCustomerMarginCallsWithQuotesDB(db, c.ID, 16, .01, now, "HK", nil); err == nil || forced {
		t.Fatal("缺有效报价时不得补仓或强平", err, forced)
	}
	quotes := map[string]positionquote.Quote{"02348.HK": {Price: 7, At: now.Unix()}}
	n, forced, err := processCustomerMarginCallsWithQuotesDB(db, c.ID, 16, .01, now, "HK", quotes)
	if err != nil || !forced || n != 0 {
		t.Fatal("港币不足应独立强平5倍港股", n, forced, err)
	}
	if err := db.First(&c, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	if c.Balance != 50000 || c.HKDBalance != -1000 || c.TotalProfit != 0 || c.TotalLoss != 0 {
		t.Fatalf("不得动人民币或抹掉穿仓亏损：HKD=%v CNY=%v", c.HKDBalance, c.Balance)
	}
	for i := range positions {
		p := &positions[i]
		if err := db.First(p, p.ID).Error; err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			_, supplemental := positionbasis.MarginParts(*p)
			if p.PositionQty != 0 || p.Margin != 0 || supplemental != 0 {
				t.Fatal("强平必须清除基础与追加担保", p)
			}
		} else if p.PositionQty == 0 {
			t.Fatal("不得强平人民币持仓或旧1倍港股", p)
		}
	}
	var flow system.CustomerFundRecord
	if err := db.First(&flow).Error; err != nil || flow.Currency != "HKD" || flow.Amount != 1000 || flow.Direction != "扣款" || flow.Balance != -1000 {
		t.Fatal("港币强平须有可核对的独立资金流水", flow, err)
	}
}

func TestFinancingMarginSchemaMigration(t *testing.T) {
	db := financingTestDB(t)
	c := system.Customer{Phone: "financing-schema", Balance: 50000, HKDBalance: 30000}
	if err := db.Create(&c).Error; err != nil {
		t.Fatal(err)
	}
	p := system.TradePosition{CustomerID: c.ID, Symbol: "000001.SZ", Currency: "CNY", QuantityBasis: 1,
		PositionQty: 1000, AvailableQty: 1000, TotalCost: 10000, CostPrice: 10, Margin: 2600, Leverage: 5, Status: 1}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Migrator().DropColumn(&system.TradePosition{}, "SupplementalMargin"); err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&system.TradePosition{}); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&p, p.ID).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.First(&c, c.ID).Error; err != nil {
		t.Fatal(err)
	}
	base, extra := positionbasis.MarginParts(p)
	if p.SupplementalMargin != nil || p.Margin != 2600 || p.PositionQty != 1000 || p.TotalCost != 10000 ||
		p.Leverage != 5 || c.Balance != 50000 || c.HKDBalance != 30000 || base != 2000 || extra != 600 {
		t.Fatal("新增字段只能拆分旧担保金展示，不得转换持仓、转出追加资金或改变账户余额", p, base, extra)
	}
}
