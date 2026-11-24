package trade

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"api-server/config"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

func TestAdjustRecordBasisPreservesFinancialSnapshotIntegration(t *testing.T) {
	dsn := os.Getenv("POSITION_BASIS_TEST_DSN")
	if dsn == "" {
		t.Skip("未配置独立持仓测试数据库")
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
	schema := fmt.Sprintf("trade_record_basis_%d", time.Now().UnixNano())
	if err := root.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	}()

	config.PgsqlDSN = dsn + " search_path=" + schema
	db := pgdb.GetClient()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := db.AutoMigrate(&system.TradeRecord{}, &system.TradeRecordBasisReview{}); err != nil {
		t.Fatal(err)
	}
	profit, cost := 123.45, 29000.0
	record := system.TradeRecord{
		CustomerID: 9, Symbol: "600000.SH", StockName: "测试股票", Currency: "CNY",
		Direction: "卖出", TradePrice: 2.9, Quantity: 10000, Amount: 29000,
		QuantityBasis: 0, RealizedProfit: &profit, RealizedCost: &cost,
		Commission: 8.7, TradeAt: time.Now().Unix(),
	}
	if err := db.Create(&record).Error; err != nil {
		t.Fatal(err)
	}

	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PUT("/records/basis", func(c *gin.Context) {
		c.Set("user_id", uint(77))
		AdjustRecordBasis(c)
	})
	body, err := json.Marshal(map[string]any{
		"id": record.ID, "quantity_basis": 1, "reason": "核对原始成交单，确认该笔为实际股数口径",
	})
	if err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPut, "/records/basis", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	if recorder.Code != http.StatusOK {
		t.Fatalf("修正接口HTTP状态错误：%d", recorder.Code)
	}

	var saved system.TradeRecord
	if err := db.First(&saved, record.ID).Error; err != nil {
		t.Fatal(err)
	}
	if saved.QuantityBasis != 1 || saved.TradePrice != record.TradePrice ||
		saved.Quantity != record.Quantity || saved.Amount != record.Amount ||
		saved.Commission != record.Commission || saved.RealizedProfit == nil ||
		*saved.RealizedProfit != profit || saved.RealizedCost == nil || *saved.RealizedCost != cost {
		t.Fatalf("修正口径不应改变成交或盈亏快照：%+v", saved)
	}
	var review system.TradeRecordBasisReview
	if err := db.Where("trade_record_id = ?", record.ID).First(&review).Error; err != nil {
		t.Fatal(err)
	}
	if review.ReviewerID != 77 || review.BeforeBasis != 0 || review.AfterBasis != 1 ||
		review.Reason != "核对原始成交单，确认该笔为实际股数口径" {
		t.Fatalf("口径修正审计内容错误：%+v", review)
	}
}
