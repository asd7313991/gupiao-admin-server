package trade

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
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
	"api-server/util/positionbasis"
)

func TestSaveManualPositionFiveTimesIntegration(t *testing.T) {
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
	schema := fmt.Sprintf("manual_position_%d", time.Now().UnixNano())
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
	if err := db.AutoMigrate(&system.Customer{}, &system.StockSecurity{}, &system.TradePosition{}, &system.TradeRecord{}, &system.CustomerFundRecord{}); err != nil {
		t.Fatal(err)
	}
	customer := system.Customer{Phone: "19900000001", Balance: 100000, FrozenBalance: 100, HKDBalance: 20000}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	securities := []system.StockSecurity{
		{Code: "001317", Symbol: "001317.SZ", Name: "测试A股", LastPrice: 52.45},
		{Code: "09889", Symbol: "09889.HK", Name: "测试港股", LastPrice: 3},
	}
	if err := db.Create(&securities).Error; err != nil {
		t.Fatal(err)
	}
	existing := system.TradePosition{
		CustomerID: customer.ID, Symbol: "001317.SZ", Currency: "CNY",
		QuantityBasis: positionbasis.Actual, PositionQty: 301, AvailableQty: 301,
		CostPrice: 52.45, CurrentPrice: 52.45, MarketValue: 15787.45,
		TotalCost: 15787.45, Margin: 15787.45, Leverage: 1, Status: 1,
	}
	if err := db.Create(&existing).Error; err != nil {
		t.Fatal(err)
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/positions", SavePosition)
	router.PUT("/positions", SavePosition)
	save := func(method string, input any) (int, system.TradePosition) {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(method, "/positions", bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		var result struct {
			Code int                        `json:"code"`
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		// 通用响应的时间是数字时间戳，此处只回读持仓数值字段。
		delete(result.Data, "created_at")
		delete(result.Data, "updated_at")
		delete(result.Data, "deleted_at")
		values, err := json.Marshal(result.Data)
		if err != nil {
			t.Fatal(err)
		}
		var position system.TradePosition
		if err := json.Unmarshal(values, &position); err != nil {
			t.Fatal(err)
		}
		return result.Code, position
	}
	near := func(actual, expected float64) {
		t.Helper()
		if math.Abs(actual-expected) > 0.000001 {
			t.Fatalf("实际=%v，预期=%v", actual, expected)
		}
	}
	for _, submittedLeverage := range []float64{0, 1, 5, 20} {
		code, saved := save(http.MethodPost, map[string]any{
			"phone": customer.Phone, "symbol": "001317.SZ", "cost_price": 52.45,
			"position_qty": 301, "leverage": submittedLeverage, "quantity_basis": 0,
		})
		if code != 200 || saved.ID == 0 {
			t.Fatalf("新增持仓失败：%d", code)
		}
		var stored system.TradePosition
		if err := db.First(&stored, saved.ID).Error; err != nil {
			t.Fatal(err)
		}
		for _, record := range []system.TradePosition{saved, stored} {
			if record.QuantityBasis != positionbasis.Actual || record.Leverage != 5 || record.Currency != "CNY" ||
				record.PositionQty != 301 || record.AvailableQty != 301 || record.CostPrice != 52.45 {
				t.Fatalf("实际股数、成本或5倍杠杆错误：%+v", record)
			}
			near(record.TotalCost, 15787.45)
			near(record.Margin, 3157.49)
			near(record.MarketValue, 15787.45)
			near(record.ProfitLoss, 0)
		}
		refreshPositionMarketValues(&stored, 53.45)
		near(stored.MarketValue, 16088.45)
		near(stored.ProfitLoss, 301)
		near(stored.Margin, 3157.49)
		near(stored.ProfitRate, positionbasis.Money(301/15787.45*100))
	}
	var untouched system.TradePosition
	if err := db.First(&untouched, existing.ID).Error; err != nil {
		t.Fatal(err)
	}
	if untouched.Leverage != 1 || untouched.Margin != 15787.45 {
		t.Fatal("新增规则不能自动修改已有1倍持仓")
	}
	code, saved := save(http.MethodPut, untouched)
	if code != 200 || saved.Leverage != 1 || saved.Margin != 15787.45 {
		t.Fatal("仅编辑已有持仓不能自动改成5倍")
	}
	if code, _ := save(http.MethodPost, map[string]any{
		"phone": customer.Phone, "symbol": "09889.HK", "cost_price": 3, "position_qty": 100,
	}); code != 400 {
		t.Fatal("港股仍应禁止手工新增")
	}
	var after system.Customer
	if err := db.First(&after, customer.ID).Error; err != nil {
		t.Fatal(err)
	}
	if after.Balance != customer.Balance || after.FrozenBalance != customer.FrozenBalance || after.HKDBalance != customer.HKDBalance {
		t.Fatal("本次修复不能改变客户余额或冻结")
	}
	for _, model := range []any{&system.TradeRecord{}, &system.CustomerFundRecord{}} {
		var count int64
		if err := db.Model(model).Count(&count).Error; err != nil || count != 0 {
			t.Fatal("手工持仓仍不应生成成交或资金流水")
		}
	}
}
