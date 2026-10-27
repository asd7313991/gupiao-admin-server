package system_test

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"api-server/util/positionbasis"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestApprovedPositionBasisConversionIntegration(t *testing.T) {
	dsn := os.Getenv("POSITION_BASIS_TEST_DSN")
	if dsn == "" {
		t.Skip("未配置持仓口径隔离测试数据库")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	conn, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	conn.SetMaxOpenConns(1)
	schema := fmt.Sprintf("conversion_test_%d", time.Now().UnixNano())
	if err := db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	}()
	if err := db.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&system.Customer{}, &system.TradePosition{}, &system.TradeRecord{}, &system.LimitOrder{}, &system.PositionBasisConversion{}); err != nil {
		t.Fatal(err)
	}
	for _, id := range []uint{1, 5, 7} {
		c := system.Customer{Phone: fmt.Sprintf("conversion-%d", id), Balance: 123456.78, FrozenBalance: 25680}
		c.ID = id
		if err := db.Create(&c).Error; err != nil {
			t.Fatal(err)
		}
	}
	specs := []struct {
		id, customer uint
		symbol       string
		qty, cost    float64
		status       uint
	}{
		{15, 7, "300274.SZ", 1000, 82.49, 1},
		{17, 5, "600814.SH", 40000, 7.07, 1},
		{18, 5, "600802.SH", 40000, 6.78, 1},
		{25, 5, "002384.SZ", 800, 114.22, 2},
		{28, 1, "002242.SZ", 300, 21.09, 1},
		{29, 1, "002747.SZ", 5000, 30.44, 1},
	}
	for _, s := range specs {
		p := system.TradePosition{CustomerID: s.customer, Symbol: s.symbol, PositionQty: s.qty, AvailableQty: s.qty,
			CostPrice: s.cost, TotalCost: positionbasis.Money(s.qty * s.cost), Margin: positionbasis.Money(s.qty * s.cost), Leverage: 5,
			CurrentPrice: s.cost + 1, Status: s.status}
		p.ID = s.id
		p.MarketValue, p.ProfitLoss, p.ProfitRate = positionbasis.Values(p, p.CurrentPrice)
		if err := db.Create(&p).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&system.TradeRecord{CustomerID: s.customer, Symbol: s.symbol, Direction: "买入", Quantity: s.qty, TradePrice: s.cost, Amount: p.TotalCost}).Error; err != nil {
			t.Fatal(err)
		}
	}
	untouched := system.TradePosition{CustomerID: 5, Symbol: "00875.HK", PositionQty: 2000, AvailableQty: 2000, CostPrice: .35, TotalCost: 700, Margin: 700, Leverage: 1, Status: 1}
	untouched.ID = 23
	if err := db.Create(&untouched).Error; err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "convert-approved-position-basis.sql"))
	if err != nil {
		t.Fatal(err)
	}
	_, block, ok := strings.Cut(string(script), "DO $$")
	if !ok {
		t.Fatal("转换脚本缺少校验事务")
	}
	block, _, ok = strings.Cut(block, "END $$;")
	if !ok {
		t.Fatal("转换脚本缺少事务结束")
	}
	block = "DO $$" + block + "END $$;"
	readPositions := func() []system.TradePosition {
		t.Helper()
		var p []system.TradePosition
		if err := db.Order("id").Find(&p).Error; err != nil {
			t.Fatal(err)
		}
		return p
	}
	before := readPositions()
	var customers []system.Customer
	var trades []system.TradeRecord
	if err := db.Order("id").Find(&customers).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Order("id").Find(&trades).Error; err != nil {
		t.Fatal(err)
	}
	rollback := errors.New("演练回滚")
	if err := db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec(block).Error; err != nil {
			return err
		}
		return rollback
	}); !errors.Is(err, rollback) {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(before, readPositions()) {
		t.Fatal("演练不得持久修改持仓")
	}
	for _, failure := range []string{"changed", "cost", "pending"} {
		err := db.Transaction(func(tx *gorm.DB) error {
			if failure == "changed" {
				if err := tx.Model(&system.TradePosition{}).Where("id = 29").Update("available_qty", 4999).Error; err != nil {
					return err
				}
			} else if failure == "cost" {
				if err := tx.Model(&system.TradePosition{}).Where("id = 29").Update("total_cost", 152200.001).Error; err != nil {
					return err
				}
			} else {
				if err := tx.Create(&system.LimitOrder{CustomerID: 1, Symbol: "002747.SZ", Status: "pending"}).Error; err != nil {
					return err
				}
			}
			return tx.Exec(block).Error
		})
		if err == nil {
			t.Fatalf("%s必须拒绝整批转换", failure)
		}
		if !reflect.DeepEqual(before, readPositions()) {
			t.Fatal("后续一笔校验失败必须撤销前面所有转换")
		}
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return tx.Exec(block).Error }); err != nil {
		t.Fatal(err)
	}
	after := readPositions()
	for i, p := range after {
		old := before[i]
		if p.ID == 23 {
			if !reflect.DeepEqual(old, p) {
				t.Fatal("港股不得被转换")
			}
			continue
		}
		if p.PositionQty != old.PositionQty*5 || p.AvailableQty != old.AvailableQty*5 || p.TotalCost != old.TotalCost*5 ||
			p.Margin != old.Margin || p.CostPrice != old.CostPrice || p.QuantityBasis != 1 || p.Status != 1 ||
			p.MarketValue != old.MarketValue || p.ProfitLoss != old.ProfitLoss {
			t.Fatalf("转换敞口或本金发生变化 before=%+v after=%+v", old, p)
		}
	}
	var actualCustomers []system.Customer
	var actualTrades []system.TradeRecord
	if err := db.Order("id").Find(&actualCustomers).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Order("id").Find(&actualTrades).Error; err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(customers, actualCustomers) || !reflect.DeepEqual(trades, actualTrades) {
		t.Fatal("账户余额/冻结/历史成交不得修改")
	}
	var auditCount int64
	if err := db.Model(&system.PositionBasisConversion{}).Count(&auditCount).Error; err != nil {
		t.Fatal(err)
	}
	if auditCount != 6 {
		t.Fatalf("缺少持久转换审计 %d", auditCount)
	}
	if err := db.Transaction(func(tx *gorm.DB) error { return tx.Exec(block).Error }); err == nil {
		t.Fatal("重复转换必须拒绝")
	}
	if !reflect.DeepEqual(after, readPositions()) {
		t.Fatal("重复操作不得二次放大数量")
	}
}
