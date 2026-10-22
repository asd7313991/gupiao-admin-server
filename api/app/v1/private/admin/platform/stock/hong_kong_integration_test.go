package stock

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"

	"api-server/db/pgdb/system"
)

func TestSaveHongKongSecurities(t *testing.T) {
	dsn := os.Getenv("HK_SYNC_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置 HK_SYNC_TEST_DSN，跳过港股采集数据库测试")
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
	rollback := errors.New("回滚隔离测试数据")
	err = db.Transaction(func(tx *gorm.DB) error {
		schema := fmt.Sprintf("hk_sync_test_%d", time.Now().UnixNano())
		if err := tx.Exec("CREATE SCHEMA " + schema).Error; err != nil {
			return err
		}
		if err := tx.Exec("SET LOCAL search_path TO " + schema).Error; err != nil {
			return err
		}
		if err := tx.AutoMigrate(&system.StockSecurity{}); err != nil {
			return err
		}
		aStock := system.StockSecurity{Code: "600519", Symbol: "600519.SH", Market: "A股", Name: "测试A股", LastPrice: 1314.54, Status: system.StatusEnabled}
		existing := system.StockSecurity{Code: "00700", Symbol: "00700.HK", Market: "港股", Name: "测试港股", LastPrice: 400, Status: system.StatusDisabled, Board: "人工分类"}
		if err := tx.Create(&[]system.StockSecurity{aStock, existing}).Error; err != nil {
			return err
		}
		rows := []system.StockSecurity{
			{Code: "00700", Symbol: "00700.HK", Market: "港股", Exchange: "港交所", Name: "腾讯控股", LastPrice: 410.123, QuoteAt: 1792476000, Source: "sina/tencent", Status: system.StatusDisabled},
			{Code: "09988", Symbol: "09988.HK", Market: "港股", Exchange: "港交所", Name: "阿里巴巴", LastPrice: 104.7, QuoteAt: 1792476001, Source: "sina", Status: system.StatusDisabled},
		}
		for run := 0; run < 2; run++ {
			if err := saveHongKongSecurities(context.Background(), tx, rows); err != nil {
				return err
			}
		}
		var saved system.StockSecurity
		if err := tx.Where("code = ?", "00700").First(&saved).Error; err != nil {
			return err
		}
		if saved.LastPrice != 410.123 || saved.Source != "sina/tencent" || saved.QuoteAt != 1792476000 || saved.Board != "人工分类" || saved.Status != system.StatusDisabled {
			t.Errorf("重复同步未保留报价或人工状态：%+v", saved)
		}
		var total int64
		if err := tx.Model(&system.StockSecurity{}).Count(&total).Error; err != nil {
			return err
		}
		if total != 3 {
			t.Errorf("重复同步产生重复记录：%d", total)
		}
		var unchanged system.StockSecurity
		if err := tx.Where("code = ?", aStock.Code).First(&unchanged).Error; err != nil {
			return err
		}
		if unchanged.LastPrice != aStock.LastPrice || unchanged.Market != "A股" || unchanged.Status != system.StatusEnabled {
			t.Errorf("港股同步修改了A股数据：%+v", unchanged)
		}
		failing := make([]system.StockSecurity, 101)
		for index := range failing {
			code := fmt.Sprintf("%05d", 20000+index)
			failing[index] = system.StockSecurity{Code: code, Symbol: code + ".HK", Market: "港股", Name: "事务测试", Status: system.StatusDisabled}
		}
		failing[100].Symbol = "00700.HK"
		if err := saveHongKongSecurities(context.Background(), tx, failing); err == nil {
			t.Error("未拒绝第二批的证券代码冲突")
		}
		if err := tx.Model(&system.StockSecurity{}).Count(&total).Error; err != nil {
			return err
		}
		if total != 3 {
			t.Errorf("批量保存失败未整体回滚：%d", total)
		}
		return rollback
	})
	if !errors.Is(err, rollback) {
		t.Fatal(err)
	}
}
