package mobile

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestHongKongLimitOrderIntegration(t *testing.T) {
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
	schema := fmt.Sprintf("hk_limit_test_%d", time.Now().UnixNano())
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
	if err := db.AutoMigrate(&system.Customer{}, &system.StockSecurity{}, &system.AppSystemSetting{}, &system.TradePosition{},
		&system.TradeRecord{}, &system.CustomerFundRecord{}, &system.HongKongTradeRequest{}, &system.LimitOrder{}); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 22, 11, 0, 0, 0, time.FixedZone("HK", 8*3600))
	customer := system.Customer{Phone: "19900009911", Balance: 12345, FrozenBalance: 789, HKDBalance: 100000, Status: 1, FundStatus: 1, Verified: 1}
	security := system.StockSecurity{Code: "02348", Symbol: "02348.HK", Market: "港股", Name: "限价测试", Status: 1}
	setting := system.AppSystemSetting{Config: `{"trade":{"allDay":true},"hkTrade":{"enabled":true,"localCloseTest":true,"commission":0.00025,"minCommission":15,"stampDuty":0.001,"regulatoryFee":0.000027,"tradingFee":0.0000565,"settlementFee":0.000042}}`}
	for _, value := range []any{&customer, &security, &setting} {
		if err := db.Create(value).Error; err != nil {
			t.Fatal(err)
		}
	}
	q := hongKongQuote{Code: security.Code, LastPrice: 0.95, LotSize: 4000, Currency: "HKD", Kind: "GP", QuoteAt: now.Add(-15 * time.Minute).Unix()}
	input := hongKongLimitInput{limitOrderInput: limitOrderInput{Code: security.Code, Direction: "买入", Quantity: 4000, LimitPrice: 1}, RequestID: "hk-limit-integration-buy"}
	assertAccount := func(hkd, frozen float64) {
		t.Helper()
		var c system.Customer
		if err := db.First(&c, customer.ID).Error; err != nil {
			t.Fatal(err)
		}
		if c.Balance != 12345 || c.FrozenBalance != 789 || c.HKDBalance != hkd || c.HKDFrozenBalance != frozen || c.TotalProfit != 0 || c.TotalLoss != 0 {
			t.Fatalf("资金隔离或冻结错误: %+v", c)
		}
	}
	var order system.LimitOrder
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := placeHongKongLimitOrderDB(db, customer.ID, security, q, input, now)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := db.Where("request_id = ?", input.RequestID).First(&order).Error; err != nil {
		t.Fatal(err)
	}
	reserved := order.FrozenAmount
	assertAccount(roundMoney(100000-reserved), reserved)
	conflict := input
	conflict.LimitPrice = 0.9
	if _, err := placeHongKongLimitOrderDB(db, customer.ID, security, q, conflict, now); err == nil {
		t.Fatal("相同编号不同委托应拒绝")
	}
	if filled, err := matchHongKongLimitOrderDB(db, customer.ID, order.ID, q, now); err != nil || filled {
		t.Fatalf("无盘口不能成交: %v %v", filled, err)
	}
	assertAccount(roundMoney(100000-reserved), reserved)
	depth := q
	depth.Ask = []orderBookLevel{{Price: 0.95, Volume: 100}}
	stale := depth
	stale.QuoteAt = now.Add(-31 * time.Minute).Unix()
	if filled, err := matchHongKongLimitOrderDB(db, customer.ID, order.ID, stale, now); err != nil || filled {
		t.Fatal("旧盘口应继续等待", filled, err)
	}
	separateTime := stale
	separateTime.Source, separateTime.DepthAt, separateTime.QuoteAt = "eastmoney", stale.QuoteAt, now.Unix()
	if filled, err := matchHongKongLimitOrderDB(db, customer.ID, order.ID, separateTime, now); err != nil || filled {
		t.Fatal("东方财富新价格不能刷新腾讯旧盘口的时间", filled, err)
	}
	assertAccount(roundMoney(100000-reserved), reserved)
	errs = make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := matchHongKongLimitOrderDB(db, customer.ID, order.ID, depth, now)
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(&order, order.ID).Error; err != nil || order.Status != limitOrderFilled || order.ExecutedPrice != 0.95 || order.FrozenAmount != 0 {
		t.Fatal("买入成交状态错误", order, err)
	}
	var record system.TradeRecord
	if err := db.First(&record, order.TradeRecordID).Error; err != nil || !strings.Contains(record.Remark, "有效买卖盘口") || record.TradePrice != 0.95 {
		t.Fatal("成交来源错误", record, err)
	}
	afterBuy := roundMoney(100000 - record.Amount - record.Commission - record.StampDuty - record.TransferFee)
	assertAccount(afterBuy, 0)
	if err := cancelHongKongLimitOrderDB(db, customer.ID, order.ID, now); err == nil {
		t.Fatal("已成交不可撤单")
	}
	buy2 := input
	buy2.RequestID = "hk-limit-integration-cancel"
	cancelOrder, err := placeHongKongLimitOrderDB(db, customer.ID, security, q, buy2, now)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := cancelHongKongLimitOrderDB(db, customer.ID, cancelOrder.ID, now); err != nil {
			t.Fatal(err)
		}
	}
	assertAccount(afterBuy, 0)
	// A股管理费撤单流程不能释放港股冻结资金。
	buy2.RequestID = "hk-limit-management-isolation"
	isolated, err := placeHongKongLimitOrderDB(db, customer.ID, security, q, buy2, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Transaction(func(tx *gorm.DB) error {
		var c system.Customer
		if err := tx.First(&c, customer.ID).Error; err != nil {
			return err
		}
		return cancelCustomerPendingOrders(tx, &c, customer.ID, now.Unix())
	}); err != nil {
		t.Fatal(err)
	}
	assertAccount(roundMoney(afterBuy-isolated.FrozenAmount), isolated.FrozenAmount)
	if err := cancelHongKongLimitOrderDB(db, customer.ID, isolated.ID, now); err != nil {
		t.Fatal(err)
	}
	var position system.TradePosition
	if err := db.Where("customer_id = ? AND symbol = ?", customer.ID, security.Symbol).First(&position).Error; err != nil {
		t.Fatal(err)
	}
	// 增加零股以验证全部可卖数量可冻结，部分零股不可冻结。
	position.PositionQty, position.AvailableQty = 4500, 4500
	if err := db.Save(&position).Error; err != nil {
		t.Fatal(err)
	}
	sell := input
	sell.Direction, sell.Quantity, sell.LimitPrice, sell.RequestID = "卖出", 500, 0.9, "hk-limit-integration-partial"
	if _, err := placeHongKongLimitOrderDB(db, customer.ID, security, q, sell, now); err == nil {
		t.Fatal("部分零股应拒绝")
	}
	sell.Quantity, sell.RequestID = 4500, "hk-limit-integration-sell"
	sellOrder, err := placeHongKongLimitOrderDB(db, customer.ID, security, q, sell, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.First(&position, position.ID).Error; err != nil || position.AvailableQty != 0 {
		t.Fatal("卖出持仓未冻结", position, err)
	}
	if _, err := placeHongKongLimitOrderDB(db, customer.ID, security, q, hongKongLimitInput{limitOrderInput: sell.limitOrderInput, RequestID: "hk-limit-double-reservation"}, now); err == nil {
		t.Fatal("重复占用持仓应拒绝")
	}
	if err := cancelHongKongLimitOrderDB(db, customer.ID+1, sellOrder.ID, now); err == nil {
		t.Fatal("不能撤销其他客户委托")
	}
	if err := cancelHongKongLimitOrderDB(db, customer.ID, sellOrder.ID, now); err != nil {
		t.Fatal(err)
	}
	sell.RequestID = "hk-limit-integration-sell-fill"
	sellOrder, err = placeHongKongLimitOrderDB(db, customer.ID, security, q, sell, now)
	if err != nil {
		t.Fatal(err)
	}
	sellDepth := q
	sellDepth.Bid = []orderBookLevel{{Price: 0.95, Volume: 100}}
	if filled, err := matchHongKongLimitOrderDB(db, customer.ID, sellOrder.ID, sellDepth, now); err != nil || !filled {
		t.Fatal("全部零股卖出成交失败", filled, err)
	}
	if err := db.First(&position, position.ID).Error; err != nil || position.PositionQty != 0 || position.AvailableQty != 0 {
		t.Fatal("卖出结算错误", position, err)
	}
	var count int64
	if err := db.Model(&system.TradeRecord{}).Count(&count).Error; err != nil || count != 2 {
		t.Fatal("成交数量错误", count, err)
	}
	var aCount int64
	if err := tradeMarketScope(db.Model(&system.LimitOrder{}), "A").Count(&aCount).Error; err != nil || aCount != 0 {
		t.Fatal("A股挂单查询混入港股", aCount, err)
	}
	if err := db.First(&customer, customer.ID).Error; err != nil {
		t.Fatal(err)
	}
	before := customer.HKDBalance
	buy2.RequestID = "hk-limit-fee-change-rollback"
	feeOrder, err := placeHongKongLimitOrderDB(db, customer.ID, security, q, buy2, now)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Model(&setting).Update("config", strings.Replace(setting.Config, `"minCommission":15`, `"minCommission":10000`, 1)).Error; err != nil {
		t.Fatal(err)
	}
	if filled, err := matchHongKongLimitOrderDB(db, customer.ID, feeOrder.ID, depth, now); err == nil || filled {
		t.Fatal("费用上涨不能动用未冻结资金", filled, err)
	}
	assertAccount(roundMoney(before-feeOrder.FrozenAmount), feeOrder.FrozenAmount)
	if err := db.Model(&setting).Update("config", strings.Replace(setting.Config, `"minCommission":10000`, `"minCommission":15`, 1)).Error; err != nil {
		t.Fatal(err)
	}
	if err := cancelHongKongLimitOrderDB(db, customer.ID, feeOrder.ID, now); err != nil {
		t.Fatal(err)
	}
	assertAccount(before, 0)
	buy2.RequestID = "hk-limit-cancel-match-race"
	raceOrder, err := placeHongKongLimitOrderDB(db, customer.ID, security, q, buy2, now)
	if err != nil {
		t.Fatal(err)
	}
	errs = make(chan error, 2)
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, err := matchHongKongLimitOrderDB(db, customer.ID, raceOrder.ID, depth, now)
		errs <- err
	}()
	go func() {
		defer wg.Done()
		err := cancelHongKongLimitOrderDB(db, customer.ID, raceOrder.ID, now)
		if err != nil && strings.Contains(err.Error(), "已成交委托不能撤销") {
			err = nil
		}
		errs <- err
	}()
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if err := db.First(&raceOrder, raceOrder.ID).Error; err != nil {
		t.Fatal(err)
	}
	switch raceOrder.Status {
	case limitOrderCancelled:
		assertAccount(before, 0)
	case limitOrderFilled:
		record = system.TradeRecord{}
		if err := db.First(&record, raceOrder.TradeRecordID).Error; err != nil {
			t.Fatal(err)
		}
		assertAccount(roundMoney(before-record.Amount-record.Commission-record.StampDuty-record.TransferFee), 0)
	default:
		t.Fatal("撤单与撮合竞争后状态错误", raceOrder)
	}
	night := now.Add(12 * time.Hour)
	buy2.RequestID = "hk-limit-all-day-night"
	nightOrder, err := placeHongKongLimitOrderDB(db, customer.ID, security, q, buy2, night)
	if err != nil {
		t.Fatal(err)
	}
	nightDepth := depth
	nightDepth.QuoteAt = night.Add(-15 * time.Minute).Unix()
	if err := db.Model(&setting).Update("config", gorm.Expr("jsonb_set(config::jsonb, '{trade,allDay}', 'false'::jsonb)")).Error; err != nil {
		t.Fatal(err)
	}
	if filled, err := matchHongKongLimitOrderDB(db, customer.ID, nightOrder.ID, nightDepth, night); err != nil || filled {
		t.Fatal("关闭全天开关后夜间应等待", filled, err)
	}
	if err := db.Model(&setting).Update("config", gorm.Expr("jsonb_set(config::jsonb, '{trade,allDay}', 'true'::jsonb)")).Error; err != nil {
		t.Fatal(err)
	}
	nightOld := nightDepth
	nightOld.QuoteAt = night.Add(-31 * time.Minute).Unix()
	if filled, err := matchHongKongLimitOrderDB(db, customer.ID, nightOrder.ID, nightOld, night); err != nil || filled {
		t.Fatal("开启全天开关仍拒绝过期盘口", filled, err)
	}
	if filled, err := matchHongKongLimitOrderDB(db, customer.ID, nightOrder.ID, nightDepth, night); err != nil || !filled {
		t.Fatal("开启全天开关应以有效夜间盘口模拟成交", filled, err)
	}
	if err := db.Model(&setting).Update("config", gorm.Expr("jsonb_set(config::jsonb, '{trade,allDay}', 'false'::jsonb)")).Error; err != nil {
		t.Fatal(err)
	}
	buy2.RequestID = "hk-limit-all-day-switched-off"
	blockedOrder, err := placeHongKongLimitOrderDB(db, customer.ID, security, q, buy2, night)
	if err != nil {
		t.Fatal(err)
	}
	if filled, err := matchHongKongLimitOrderDB(db, customer.ID, blockedOrder.ID, nightDepth, night); err != nil || filled {
		t.Fatal("重新关闭全天开关须恢复时段限制", filled, err)
	}
	if err := cancelHongKongLimitOrderDB(db, customer.ID, blockedOrder.ID, night); err != nil {
		t.Fatal(err)
	}
}
