package mobile

import (
	"errors"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestCurrencyExchangeIntegration(t *testing.T) {
	dsn := os.Getenv("EXCHANGE_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 EXCHANGE_TEST_DSN，跳过数据库测试")
	}
	// 每次测试独立模式（schema），不使用或改动业务客户。
	schema := fmt.Sprintf("exchange_test_%d", time.Now().UnixNano())
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	rootSQL, err := root.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer rootSQL.Close()
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
	if err := db.AutoMigrate(&system.Customer{}, &system.CustomerFundRecord{}, &system.CurrencyExchange{}, &system.AppSystemSetting{}); err != nil {
		t.Fatal(err)
	}
	hash, err := system.HashPassword("123456")
	if err != nil {
		t.Fatal(err)
	}
	customer := system.Customer{Phone: "19900000001", Balance: 1000, StrategyBalance: 10, FrozenBalance: 20,
		Status: system.StatusEnabled, FundStatus: system.StatusEnabled, Verified: system.StatusEnabled, TradePassword: hash}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	config := system.AppSystemSetting{Config: `{"links":{"hkdRate":0.89}}`}
	if err := db.Create(&config).Error; err != nil {
		t.Fatal(err)
	}
	input := exchangeInput{RequestID: "exchange-request-0001", From: "CNY", Amount: 100, Rate: 0.89, TradePIN: "123456"}
	run := func(input exchangeInput) (system.CurrencyExchange, error) {
		var row system.CurrencyExchange
		err := db.Transaction(func(tx *gorm.DB) error {
			var err error
			row, err = executeCurrencyExchange(tx, customer.ID, input)
			return err
		})
		return row, err
	}
	assertBalances := func(cny, hkd float64, records, flows int64) {
		t.Helper()
		var current system.Customer
		if err := db.First(&current, customer.ID).Error; err != nil {
			t.Fatal(err)
		}
		if current.Balance != cny || current.HKDBalance != hkd || current.StrategyBalance != 10 || current.FrozenBalance != 20 {
			t.Fatalf("余额错误: CNY=%v HKD=%v 策略=%v 冻结=%v", current.Balance, current.HKDBalance, current.StrategyBalance, current.FrozenBalance)
		}
		var count int64
		if err := db.Model(&system.CurrencyExchange{}).Count(&count).Error; err != nil || count != records {
			t.Fatalf("兑换记录数量 %v err=%v", count, err)
		}
		if err := db.Model(&system.CustomerFundRecord{}).Count(&count).Error; err != nil || count != flows {
			t.Fatalf("流水数量 %v err=%v", count, err)
		}
	}
	// 相同请求并发只记一次账，不同请求竞争余额时不能透支。
	var wg sync.WaitGroup
	errs := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := run(input); errs <- err }()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	assertBalances(900, 112.35, 1, 2)
	var ledger []system.CustomerFundRecord
	if err := db.Order("id").Find(&ledger).Error; err != nil {
		t.Fatal(err)
	}
	if ledger[0].Amount != -100 || ledger[0].Currency != "CNY" || ledger[0].Direction != "扣款" || ledger[0].Balance != 900 ||
		ledger[1].Amount != 112.35 || ledger[1].Currency != "HKD" || ledger[1].Direction != "入账" || ledger[1].Balance != 112.35 {
		t.Fatalf("双币种流水错误: %+v", ledger)
	}
	for _, test := range []struct {
		name  string
		input exchangeInput
	}{
		{"幂等请求内容冲突", exchangeInput{"exchange-request-0001", "CNY", 200, 0.89, "123456"}},
		{"过期汇率", exchangeInput{"exchange-request-0002", "CNY", 100, 0.85, "123456"}},
		{"错误密码", exchangeInput{"exchange-request-0003", "CNY", 100, 0.89, "000000"}},
		{"余额不足", exchangeInput{"exchange-request-0004", "CNY", 901, 0.89, "123456"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := run(test.input); err == nil {
				t.Fatal("错误请求不应成功")
			}
			assertBalances(900, 112.35, 1, 2)
		})
	}
	reverse := exchangeInput{"exchange-request-reverse", "HKD", 100, 0.89, "123456"}
	if row, err := run(reverse); err != nil || row.Received != 89 {
		t.Fatalf("反向兑换: %+v err=%v", row, err)
	}
	assertBalances(989, 12.35, 2, 4)
	rollback := errors.New("模拟流水失败")
	err = db.Transaction(func(tx *gorm.DB) error {
		if _, err := executeCurrencyExchange(tx, customer.ID, exchangeInput{"exchange-request-rollback", "CNY", 100, 0.89, "123456"}); err != nil {
			return err
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	assertBalances(989, 12.35, 2, 4)
	if err := db.Exec("ALTER TABLE customer_fund_records ADD CONSTRAINT reject_exchange_hkd CHECK (currency <> 'HKD') NOT VALID").Error; err != nil {
		t.Fatal(err)
	}
	if _, err := run(exchangeInput{"exchange-request-ledger-failure", "CNY", 100, 0.89, "123456"}); err == nil {
		t.Fatal("流水落库失败时整笔兑换应回滚")
	}
	if err := db.Exec("ALTER TABLE customer_fund_records DROP CONSTRAINT reject_exchange_hkd").Error; err != nil {
		t.Fatal(err)
	}
	assertBalances(989, 12.35, 2, 4)
	for _, field := range []string{"verified", "status", "fund_status"} {
		if err := db.Model(&customer).Update(field, system.StatusDisabled).Error; err != nil {
			t.Fatal(err)
		}
		if _, err := run(exchangeInput{"exchange-request-locked-account", "CNY", 100, 0.89, "123456"}); err == nil {
			t.Fatalf("账户 %s 不可用时应拒绝", field)
		}
		if err := db.Model(&customer).Update(field, system.StatusEnabled).Error; err != nil {
			t.Fatal(err)
		}
		assertBalances(989, 12.35, 2, 4)
	}
	var successes int
	var mutex sync.Mutex
	wg = sync.WaitGroup{}
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			_, err := run(exchangeInput{fmt.Sprintf("exchange-request-competition-%d", i), "CNY", 600, 0.89, "123456"})
			mutex.Lock()
			defer mutex.Unlock()
			if err == nil {
				successes++
			} else if !errors.Is(err, errInsufficientExchangeBalance) {
				t.Error(err)
			}
		}(i)
	}
	wg.Wait()
	if successes != 1 {
		t.Fatalf("并发超额扣款次数=%d，预期1", successes)
	}
	assertBalances(389, 686.5, 3, 6)
	if err := db.Model(&config).Update("config", `{"links":{"hkdRate":0}}`).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := run(exchangeInput{"exchange-request-invalid-rate", "CNY", 100, 0.89, "123456"}); err == nil {
		t.Fatal("未配置汇率应拒绝")
	}
	// 已完成请求即使后台改了汇率仍返回原成交结果，不会再次扣款。
	if row, err := run(input); err != nil || row.Rate != 0.89 || row.Received != 112.35 {
		t.Fatalf("幂等重放 %+v err=%v", row, err)
	}
	assertBalances(389, 686.5, 3, 6)
}
