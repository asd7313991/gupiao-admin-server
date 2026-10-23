package stock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestASnapshotSQLNeverReplacesNewQuoteWithOlderSnapshot(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=127.0.0.1 user=test dbname=test sslmode=disable"),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "code"}}, DoUpdates: aStockSnapshotUpdates(priorityTestTime())}).
			Create(&system.StockSecurity{Code: "002384", Symbol: "002384.SZ", LastPrice: 112.3, QuoteAt: priorityTestTime().Unix()})
	})
	for _, column := range []string{"last_price", "change_rate", "volume", "amount", "turnover", "source", "quote_at", "updated_at"} {
		assignment := `"` + column + `"=CASE WHEN EXCLUDED.last_price > 0 AND EXCLUDED.quote_at > 0`
		if !strings.Contains(sql, assignment) || !strings.Contains(sql, "EXCLUDED.quote_at >= stock_securities.quote_at") {
			t.Fatalf("A股慢任务必须保护每一个行情字段不回退：%s", sql)
		}
	}
}

func TestPriorityQuotePersistenceIntegration(t *testing.T) {
	dsn := os.Getenv("HK_SYNC_TEST_DSN")
	if dsn == "" {
		t.Skip("设置 HK_SYNC_TEST_DSN 后验证重点范围、价格防回退及原状态保留")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	rollback := errors.New("回滚重点行情隔离测试")
	err = db.Transaction(func(tx *gorm.DB) error {
		schema := fmt.Sprintf("priority_quote_test_%d", time.Now().UnixNano())
		if err := tx.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL search_path TO " + schema).Error; err != nil {
			return err
		}
		if err := tx.AutoMigrate(&system.StockSecurity{}, &system.TradePosition{}, &system.CustomerWatchlist{}); err != nil {
			return err
		}
		now := priorityTestTime()
		items := []system.StockSecurity{
			{Code: "002384", Symbol: "002384.SZ", Market: "A股", LastPrice: 112.3, QuoteAt: now.Unix(), Source: "eastmoney", Status: system.StatusDisabled},
			{Code: "00700", Symbol: "00700.HK", Market: "港股", LastPrice: 500, QuoteAt: now.Unix(), Source: "tencent", Status: system.StatusDisabled},
			{Code: "300001", Symbol: "300001.SZ", Market: "A股", LastPrice: 10, QuoteAt: now.Unix()},
		}
		if err := tx.Create(&items).Error; err != nil {
			return err
		}
		holding := system.TradePosition{CustomerID: 42, Symbol: items[0].Symbol, PositionQty: 100, AvailableQty: 100}
		watch := system.CustomerWatchlist{CustomerID: 42, SecurityID: items[1].ID}
		if err := tx.Create(&holding).Error; err != nil {
			return err
		}
		if err := tx.Create(&watch).Error; err != nil {
			return err
		}
		for _, market := range []string{"A", "HK"} {
			var found []system.StockSecurity
			if err := priorityTargets(tx, market, nil).Find(&found).Error; err != nil || len(found) != 1 {
				t.Fatalf("持仓、自选及市场范围错误：%s，%+v，%v", market, found, err)
			}
		}
		var active []system.StockSecurity
		if err := priorityTargets(tx, "A", []string{items[2].Symbol}).Find(&active).Error; err != nil || len(active) != 2 {
			t.Fatalf("近期查看股票必须进入重点范围：%+v，%v", active, err)
		}
		for index, original := range items[:2] {
			old := original
			old.LastPrice, old.QuoteAt = 999, now.Add(-time.Minute).Unix()
			if err := savePriorityQuotes(context.Background(), tx, []system.StockSecurity{old}); err != nil {
				return err
			}
			if index == 0 {
				if err := saveASecurities(context.Background(), tx, []system.StockSecurity{old}); err != nil {
					return err
				}
			} else if err := saveHongKongSecurities(context.Background(), tx, []system.StockSecurity{old}); err != nil {
				return err
			}
			var saved system.StockSecurity
			if err := tx.First(&saved, original.ID).Error; err != nil {
				return err
			}
			if saved.LastPrice != original.LastPrice || saved.QuoteAt != original.QuoteAt || saved.Status != system.StatusDisabled {
				t.Fatalf("高频和全市场任务均不得回退新价或改变人工状态：%+v", saved)
			}
			newer := original
			newer.LastPrice, newer.QuoteAt = 111, now.Add(time.Second).Unix()
			if err := savePriorityQuotes(context.Background(), tx, []system.StockSecurity{newer}); err != nil {
				return err
			}
			if err := tx.First(&saved, original.ID).Error; err != nil {
				return err
			}
			if saved.LastPrice != 111 || saved.QuoteAt != newer.QuoteAt || saved.Status != system.StatusDisabled {
				t.Fatalf("应只更新真实新报价，不修改证券状态：%+v", saved)
			}
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
}
