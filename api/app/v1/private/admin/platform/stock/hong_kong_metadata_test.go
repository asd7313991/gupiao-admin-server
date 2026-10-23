package stock

import (
	"strings"
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func TestHongKongSnapshotSQLKeepsMetadataIndependentFromPrice(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=127.0.0.1 user=test dbname=test sslmode=disable"),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "code"}}, DoUpdates: hongKongSnapshotUpdates(time.Now())}).
			Create(&system.StockSecurity{Code: "00875", Symbol: "00875.HK", HKLotSize: 2000, HKCurrency: "HKD", HKKind: "GP", HKMetadataAt: 1})
	})
	for _, column := range []string{"hk_lot_size", "hk_currency", "hk_kind", "hk_metadata_at"} {
		if !strings.Contains(sql, `"`+column+`"=CASE WHEN EXCLUDED.hk_lot_size >= 1`) ||
			!strings.Contains(sql, "EXCLUDED.hk_metadata_at >= stock_securities.hk_metadata_at") {
			t.Fatalf("独立资料更新不能被空值、旧值覆盖：%s", sql)
		}
	}
	if !strings.Contains(sql, "EXCLUDED.quote_at > 0 AND EXCLUDED.quote_at <=") ||
		!strings.Contains(sql, "EXCLUDED.quote_at >= stock_securities.quote_at") ||
		strings.Contains(sql, `"status"=`) {
		t.Fatalf("价格不得倒退、采用未来时间或覆盖人工停用：%s", sql)
	}
}
