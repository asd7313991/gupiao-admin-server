package mobile

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
	"gorm.io/gorm/logger"
)

func TestHongKongTradeIntegration(t *testing.T) {
	dsn := os.Getenv("HK_TRADE_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 HK_TRADE_TEST_DSN")
	}
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	rootSQL, err := root.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer rootSQL.Close()
	schema := fmt.Sprintf("hk_trade_test_%d", time.Now().UnixNano())
	if err := root.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	}()
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := db.AutoMigrate(&system.Customer{}, &system.StockSecurity{}, &system.AppSystemSetting{}, &system.TradePosition{}, &system.TradeRecord{}, &system.CustomerFundRecord{}, &system.HongKongTradeRequest{}, &system.DailyManagementFee{}); err != nil {
		t.Fatal(err)
	}
	customer := system.Customer{Phone: "19900000001", Balance: 767676.71, HKDBalance: 100000, FrozenBalance: 30, TotalProfit: 45, TotalLoss: 67, Status: 1, FundStatus: 1, Verified: 1}
	security := system.StockSecurity{Code: "03774", Symbol: "03774.HK", Market: "港股", Name: "测试港股", LastPrice: 35.5, Status: 1}
	aStock := system.StockSecurity{Code: "600519", Symbol: "600519.SH", Market: "A股", Name: "测试A股", LastPrice: 100, Status: 1}
	setting := system.AppSystemSetting{Config: `{"hkTrade":{"enabled":true,"commission":0.00025,"minCommission":15,"stampDuty":0.001,"regulatoryFee":0.000027,"tradingFee":0.0000565,"settlementFee":0.000042}}`}
	for _, row := range []any{&customer, &security, &aStock, &setting} {
		if err := db.Create(row).Error; err != nil {
			t.Fatal(err)
		}
	}
	now := time.Date(2026, 10, 22, 11, 0, 0, 0, time.FixedZone("HK", 8*3600))
	quote := hongKongQuote{Code: "03774", LastPrice: 35.5, LotSize: 1000, Currency: "HKD", Kind: "GP", QuoteAt: now.Add(-15 * time.Minute).Unix(), Source: "tencent"}
	runWithMode := func(input hongKongOrderInput, q hongKongQuote, localRequest bool) (orderResult, error) {
		var result orderResult
		err := db.Transaction(func(tx *gorm.DB) error {
			var c system.Customer
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&c, customer.ID).Error; err != nil {
				return err
			}
			var err error
			result, err = executeHongKongOrder(tx, &c, security, q, input, now, localRequest)
			return err
		})
		return result, err
	}
	run := func(input hongKongOrderInput, q hongKongQuote) (orderResult, error) {
		return runWithMode(input, q, false)
	}
	buy := hongKongOrderInput{orderInput: orderInput{Code: "03774", Direction: "买入", Quantity: 1000}, RequestID: "hk-test-request-buy"}
	var wg sync.WaitGroup
	errs := make(chan error, 5)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := run(buy, quote); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertState := func(balance, qty float64, records int64) {
		t.Helper()
		var c system.Customer
		if err := db.First(&c, customer.ID).Error; err != nil {
			t.Fatal(err)
		}
		if c.HKDBalance != balance || c.Balance != 767676.71 || c.FrozenBalance != 30 || c.TotalProfit != 45 || c.TotalLoss != 67 {
			t.Fatalf("账户隔离错误: %+v", c)
		}
		var p system.TradePosition
		if err := db.Where("symbol = ?", security.Symbol).First(&p).Error; err != nil {
			t.Fatal(err)
		}
		if p.PositionQty != qty || p.AvailableQty != qty || p.Currency != "HKD" || p.Leverage != 1 {
			t.Fatalf("持仓错误 %+v", p)
		}
		var count int64
		for _, model := range []any{&system.TradeRecord{}, &system.CustomerFundRecord{}, &system.HongKongTradeRequest{}} {
			if err := db.Model(model).Count(&count).Error; err != nil || count != records {
				t.Fatalf("数量错误 %T count=%d err=%v", model, count, err)
			}
		}
	}
	assertState(64445.04, 1000, 1)
	for _, tc := range []struct {
		name   string
		change func(*hongKongOrderInput, *hongKongQuote)
	}{
		{"不足一手", func(i *hongKongOrderInput, q *hongKongQuote) { i.Quantity = 100 }},
		{"余额不足", func(i *hongKongOrderInput, q *hongKongQuote) { i.Quantity = 10000 }},
		{"卖出不足", func(i *hongKongOrderInput, q *hongKongQuote) { i.Direction = "卖出"; i.Quantity = 2000 }},
		{"零数量", func(i *hongKongOrderInput, q *hongKongQuote) { i.Quantity = 0 }},
		{"过期报价", func(i *hongKongOrderInput, q *hongKongQuote) { q.QuoteAt = now.Add(-31 * time.Minute).Unix() }},
		{"人民币柜台", func(i *hongKongOrderInput, q *hongKongQuote) { q.Currency = "CNY" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			input := buy
			input.RequestID = "hk-test-rejected-" + tc.name
			q := quote
			tc.change(&input, &q)
			if _, err := run(input, q); err == nil {
				t.Fatal("应拒绝")
			}
			assertState(64445.04, 1000, 1)
		})
	}
	conflict := buy
	conflict.Quantity = 2000
	if _, err := run(conflict, quote); err == nil {
		t.Fatal("同编号不同内容应拒绝")
	}
	for _, field := range []string{"status", "fund_status", "verified"} {
		if err := db.Model(&customer).Update(field, 2).Error; err != nil {
			t.Fatal(err)
		}
		input := buy
		input.RequestID = "hk-account-rejected-" + field
		if _, err := run(input, quote); err == nil {
			t.Fatal("异常账户应拒绝")
		}
		if err := db.Model(&customer).Update(field, 1).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Model(&security).Update("status", 2).Error; err != nil {
		t.Fatal(err)
	}
	blocked := buy
	blocked.RequestID = "hk-disabled-security"
	if _, err := run(blocked, quote); err == nil {
		t.Fatal("停用证券应拒绝")
	}
	if err := db.Model(&security).Update("status", 1).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("ALTER TABLE customer_fund_records ADD CONSTRAINT reject_hkd CHECK (currency <> 'HKD') NOT VALID").Error; err != nil {
		t.Fatal(err)
	}
	rollback := buy
	rollback.RequestID = "hk-rollback-failed-ledger"
	if _, err := run(rollback, quote); err == nil {
		t.Fatal("流水失败应整笔回滚")
	}
	if err := db.Exec("ALTER TABLE customer_fund_records DROP CONSTRAINT reject_hkd").Error; err != nil {
		t.Fatal(err)
	}
	assertState(64445.04, 1000, 1)
	sell := buy
	sell.RequestID = "hk-test-request-sell"
	sell.Direction = "卖出"
	sellQuote := quote
	sellQuote.LastPrice = 36
	result, err := run(sell, sellQuote)
	if err != nil || result.TotalFee != 55.52 || result.Currency != "HKD" {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	assertState(100389.52, 0, 2)
	if err := db.Model(&setting).Update("config", `{"hkTrade":{"enabled":false}}`).Error; err != nil {
		t.Fatal(err)
	}
	input := buy
	input.RequestID = "hk-gate-disabled"
	if _, err := run(input, quote); err == nil {
		t.Fatal("开关关闭应拒绝")
	}
	// 已成交回执不因开关或行情变化而重复执行。
	expired := quote
	expired.QuoteAt = 1
	if _, err := run(buy, expired); err != nil {
		t.Fatal(err)
	}
	assertState(100389.52, 0, 2)
	var hkCount, aCount int64
	if err := tradeMarketScope(db.Model(&system.TradeRecord{}), "HK").Count(&hkCount).Error; err != nil {
		t.Fatal(err)
	}
	if err := tradeMarketScope(db.Model(&system.TradeRecord{}), "A").Count(&aCount).Error; err != nil {
		t.Fatal(err)
	}
	if hkCount != 2 || aCount != 0 {
		t.Fatalf("市场隔离 HK=%d A=%d", hkCount, aCount)
	}
	var rejection exchangeRejection
	if !errors.As(exchangeRejection("拒绝"), &rejection) {
		t.Fatal("拒绝错误类型无效")
	}
	if err := db.Model(&system.TradePosition{}).Where("symbol = ?", security.Symbol).
		Updates(map[string]any{"position_qty": 1000, "available_qty": 1000, "current_price": 36, "cost_price": 100, "total_cost": 100000, "margin": 100000, "status": 1}).Error; err != nil {
		t.Fatal(err)
	}
	if forced, err := processCustomerManagementFeeDB(db, customer.ID, "2026-10-22", 0.00028, now); err != nil || forced {
		t.Fatalf("港股不应触发A股管理费或强平: forced=%v err=%v", forced, err)
	}
	if count, forced, err := processCustomerMarginCallsDB(db, customer.ID, 16, 0.01, now); err != nil || forced || count != 0 {
		t.Fatalf("港股不应触发A股补仓: count=%d forced=%v err=%v", count, forced, err)
	}
	assertState(100389.52, 1000, 2)
	if err := db.Model(&setting).Update("config", `{"hkTrade":{"enabled":true,"localCloseTest":true,"commission":0.00025,"minCommission":15,"stampDuty":0.001,"regulatoryFee":0.000027,"tradingFee":0.0000565,"settlementFee":0.000042}}`).Error; err != nil {
		t.Fatal(err)
	}
	now = time.Date(2026, 10, 22, 17, 10, 0, 0, now.Location())
	closeQuote := quote
	closeQuote.QuoteAt = time.Date(2026, 10, 22, 16, 8, 42, 0, now.Location()).Unix()
	closeBuy := buy
	closeBuy.RequestID = "hk-local-close-buy"
	if _, err := run(closeBuy, closeQuote); err == nil {
		t.Fatal("非本机请求不得使用旧收市价")
	}
	closeResult, err := runWithMode(closeBuy, closeQuote, true)
	if err != nil || !closeResult.LocalCloseTest || closeResult.QuoteAt != closeQuote.QuoteAt {
		t.Fatalf("收市价测试回执=%+v err=%v", closeResult, err)
	}
	assertState(64834.56, 2000, 3)
	var closeRecord system.TradeRecord
	if err := db.First(&closeRecord, closeResult.RecordID).Error; err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(closeRecord.Remark, "本机收市价模拟测试") || !strings.Contains(closeRecord.Remark, "16:08:42") {
		t.Fatal("成交缺少测试标识或实际报价时间")
	}
	replayed, err := runWithMode(closeBuy, closeQuote, false)
	if err != nil || replayed.RecordID != closeResult.RecordID || !replayed.LocalCloseTest {
		t.Fatalf("收市价回执重放失败: %+v %v", replayed, err)
	}
	now = now.AddDate(0, 0, 1)
	closeBuy.RequestID = "hk-local-close-next-day"
	if _, err := runWithMode(closeBuy, closeQuote, true); err == nil {
		t.Fatal("次日不得继续使用昨日收市价")
	}
	assertState(64834.56, 2000, 3)
}
