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
	"gorm.io/gorm/logger"
)

func TestPositionBasisIntegration(t *testing.T) {
	dsn := os.Getenv("POSITION_BASIS_TEST_DSN")
	if dsn == "" {
		t.Skip("未配置持仓口径隔离测试数据库")
	}
	db, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	schema := fmt.Sprintf("basis_test_%d", time.Now().UnixNano())
	if err := db.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := db.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	}()
	sqlDB.SetMaxOpenConns(1)
	if err := db.Exec("SET search_path TO " + schema).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&system.Customer{}, &system.TradePosition{}, &system.TradeRecord{}, &system.CustomerFundRecord{}, &system.LimitOrder{}, &system.StockSecurity{}); err != nil {
		t.Fatal(err)
	}
	customer := system.Customer{Phone: "basis-test", Balance: 10000}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	settings := mobileTradeSettings{}
	settings.Risk.AppLeverageEnabled, settings.Risk.DefaultLeverage = true, 5
	security := system.StockSecurity{Code: "002242", Symbol: "002242.SZ", Name: "测试九阳", LastPrice: 10}
	run := func(direction string, quantity, price float64, orders ...system.LimitOrder) (orderResult, error) {
		t.Helper()
		security.LastPrice = price
		var result orderResult
		var rejection string
		err := db.Transaction(func(tx *gorm.DB) error {
			if err := tx.First(&customer, customer.ID).Error; err != nil {
				return err
			}
			return executeMobileOrder(tx, &customer, security, orderInput{Direction: direction, Quantity: quantity}, settings, &result, &rejection, orders...)
		})
		return result, err
	}
	check := func(quantity, cost, margin, balance float64, status uint) system.TradePosition {
		t.Helper()
		var p system.TradePosition
		if err := db.Where("customer_id = ?", customer.ID).First(&p).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&customer, customer.ID).Error; err != nil {
			t.Fatal(err)
		}
		if p.QuantityBasis != positionbasis.Actual || p.PositionQty != quantity || p.TotalCost != cost ||
			p.Margin != margin || customer.Balance != balance || p.Status != status {
			t.Fatalf("账务不符 持仓=%+v balance=%v", p, customer.Balance)
		}
		return p
	}
	if _, err := run("买入", 1000, 10); err != nil {
		t.Fatal(err)
	}
	p := check(1000, 10000, 2000, 8000, system.StatusEnabled)
	view := valueCustomerPosition(p, positionquote.Quote{Price: 11})
	if view.MarketValue != 11000 || view.ProfitLoss != 1000 || view.ProfitRate != 10 {
		t.Fatalf("估值%+v", view)
	}
	if _, err := run("买入", 1000, 12); err != nil {
		t.Fatal(err)
	}
	check(2000, 22000, 4400, 5600, system.StatusEnabled)
	if _, err := run("卖出", 500, 13); err != nil {
		t.Fatal(err)
	}
	check(1500, 16500, 3300, 7700, system.StatusEnabled)
	if _, err := run("卖出", 1500, 11); err != nil {
		t.Fatal(err)
	}
	check(0, 0, 0, 11000, system.StatusDisabled)
	// 重新买入须恢复启用，委托杠杆快照不随当前设置改变。
	settings.Risk.DefaultLeverage = 8
	if _, err := run("买入", 1000, 10, system.LimitOrder{QuantityBasis: positionbasis.Actual, Leverage: 5}); err != nil {
		t.Fatal(err)
	}
	p = check(1000, 10000, 2000, 9000, system.StatusEnabled)
	if p.Leverage != 5 {
		t.Fatal("委托必须使用5倍快照")
	}
	if _, err := run("买入", 1000, 10, system.LimitOrder{QuantityBasis: positionbasis.Legacy, Leverage: 5}); err == nil {
		t.Fatal("不能将旧口径委托混入实际股数持仓")
	}
	check(1000, 10000, 2000, 9000, system.StatusEnabled)
	if _, err := run("买入", 1000000, 10); err == nil {
		t.Fatal("资金不足不得成交")
	}
	check(1000, 10000, 2000, 9000, system.StatusEnabled)
	var records []system.TradeRecord
	if err := db.Find(&records).Error; err != nil {
		t.Fatal(err)
	}
	if len(records) != 5 {
		t.Fatalf("失败交易不得留下成交记录: %d", len(records))
	}
	for _, r := range records {
		if r.QuantityBasis != positionbasis.Actual || r.Leverage != 5 {
			t.Fatalf("成交快照丢失%+v", r)
		}
	}
	// 未转换持仓保持旧本金扣款及五倍卖出结算，不能直接套新公式。
	p.QuantityBasis, p.PositionQty, p.AvailableQty, p.TotalCost, p.CostPrice, p.Margin = positionbasis.Legacy, 100, 100, 1000, 10, 1000
	if err := db.Save(&p).Error; err != nil {
		t.Fatal(err)
	}
	if _, err := run("卖出", 100, 11); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&customer, customer.ID).Error; err != nil {
		t.Fatal(err)
	}
	if customer.Balance != 10500 {
		t.Fatalf("旧持仓应返本金1000+盈利500，实际余额%v", customer.Balance)
	}
	if _, err := run("买入", 100, 10, system.LimitOrder{QuantityBasis: positionbasis.Legacy, Leverage: 5}); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&p, p.ID).Error; err != nil {
		t.Fatal(err)
	}
	if p.Leverage != 5 || p.Margin != 1000 || p.QuantityBasis != positionbasis.Legacy {
		t.Fatal("新建旧口径委托也必须保留原5倍快照及全额本金")
	}
	if _, err := run("卖出", 100, 11); err != nil {
		t.Fatal(err)
	}
	if _, err := run("买入", 100, 10, system.LimitOrder{QuantityBasis: positionbasis.Legacy}); err != nil {
		t.Fatal(err)
	}
	if err := db.First(&p, p.ID).Error; err != nil {
		t.Fatal(err)
	}
	if p.Leverage != 8 || p.Margin != 1000 || p.QuantityBasis != positionbasis.Legacy {
		t.Fatal("迁移前无杠杆快照的旧委托应保持原设置回退，不伪造1倍")
	}

	t.Run("实际股数限价冻结撤单成交与自动强平", func(t *testing.T) {
		customer := system.Customer{Phone: "basis-limit-test", Balance: 10000, Status: system.StatusEnabled,
			FundStatus: system.StatusEnabled, Verified: system.StatusEnabled}
		if err := db.Create(&customer).Error; err != nil {
			t.Fatal(err)
		}
		settings := mobileTradeSettings{}
		settings.Risk.AppLeverageEnabled, settings.Risk.DefaultLeverage = true, 5
		settings.Trade.BuyCommission, settings.Trade.SellCommission = .001, .001
		security.LastPrice = 9
		if err := db.Create(&security).Error; err != nil {
			t.Fatal(err)
		}
		checkBalance := func(balance, frozen float64) {
			t.Helper()
			if err := db.First(&customer, customer.ID).Error; err != nil {
				t.Fatal(err)
			}
			if customer.Balance != balance || customer.FrozenBalance != frozen {
				t.Fatalf("余额/冻结不符 %v/%v，预期%v/%v", customer.Balance, customer.FrozenBalance, balance, frozen)
			}
		}
		place := func(direction string, quantity, price float64) system.LimitOrder {
			t.Helper()
			order, rejection, err := placeAStockLimitOrderDB(db, customer.ID, security, limitOrderInput{Direction: direction, Quantity: quantity, LimitPrice: price}, settings)
			if err != nil {
				t.Fatalf("挂单失败 %s %v", rejection, err)
			}
			if order.QuantityBasis != positionbasis.Actual || order.Leverage != 5 {
				t.Fatalf("快照错误 %+v", order)
			}
			return order
		}
		cancel := func(order *system.LimitOrder) {
			t.Helper()
			if err := db.Transaction(func(tx *gorm.DB) error {
				if err := releaseLimitOrderFreeze(tx, order); err != nil {
					return err
				}
				order.Status = limitOrderCancelled
				return tx.Save(order).Error
			}); err != nil {
				t.Fatal(err)
			}
		}
		fill := func(order *system.LimitOrder) error {
			t.Helper()
			return db.Transaction(func(tx *gorm.DB) error {
				filled, err := fillAStockLimitOrder(tx, order, security, settings)
				if err == nil && !filled {
					return fmt.Errorf("有效盘口委托未成交")
				}
				return err
			})
		}
		order := place("买入", 1000, 10)
		if order.FrozenAmount != 2010 {
			t.Fatalf("只冻结本金2000+佣金10，实际%v", order.FrozenAmount)
		}
		checkBalance(7990, 2010)
		cancel(&order)
		checkBalance(10000, 0)
		order = place("买入", 1000, 10)
		settings.Risk.DefaultLeverage = 8
		if err := fill(&order); err != nil {
			t.Fatal(err)
		}
		checkBalance(8191, 0)
		if order.Status != limitOrderFilled || order.ExecutedPrice != 9 || order.FrozenAmount != 0 {
			t.Fatalf("委托状态错误 %+v", order)
		}
		var p system.TradePosition
		if err := db.Where("customer_id = ?", customer.ID).First(&p).Error; err != nil {
			t.Fatal(err)
		}
		if p.PositionQty != 1000 || p.TotalCost != 9000 || p.Margin != 1800 || p.Leverage != 5 {
			t.Fatalf("成交价与本金快照错误 %+v", p)
		}
		order = place("卖出", 500, 11)
		if err := db.First(&p, p.ID).Error; err != nil {
			t.Fatal(err)
		}
		if p.AvailableQty != 500 || order.FrozenQuantity != 500 {
			t.Fatalf("可卖数量冻结错误 %+v %+v", p, order)
		}
		cancel(&order)
		if err := db.First(&p, p.ID).Error; err != nil {
			t.Fatal(err)
		}
		if p.AvailableQty != 1000 {
			t.Fatal("撤单须返还实际可卖股数")
		}
		order = place("卖出", 500, 11)
		security.LastPrice = 11
		if err := fill(&order); err != nil {
			t.Fatal(err)
		}
		checkBalance(10085.5, 0)
		if err := db.First(&p, p.ID).Error; err != nil {
			t.Fatal(err)
		}
		if p.PositionQty != 500 || p.AvailableQty != 500 || p.Margin != 900 || p.TotalCost != 4500 {
			t.Fatalf("部分卖出结算错误 %+v", p)
		}
		order = place("卖出", 500, 11)
		if err := db.Model(&p).Update("quantity_basis", positionbasis.Legacy).Error; err != nil {
			t.Fatal(err)
		}
		if err := fill(&order); err == nil {
			t.Fatal("口径不匹配卖单必须拒绝")
		}
		checkBalance(10085.5, 0)
		if err := db.First(&p, p.ID).Error; err != nil {
			t.Fatal(err)
		}
		if p.PositionQty != 500 || p.AvailableQty != 0 {
			t.Fatal("失败成交必须撤销可卖数临时解冻")
		}
		if err := db.Model(&p).Update("quantity_basis", positionbasis.Actual).Error; err != nil {
			t.Fatal(err)
		}
		cancel(&order)
		if err := db.First(&p, p.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Model(&security).Update("last_price", 11).Error; err != nil {
			t.Fatal(err)
		}
		marketValue := currentPositionsMarketValue(db, []system.TradePosition{p})
		if marketValue != 5500 || dailyManagementFee(marketValue, .00028) != 1.54 {
			t.Fatalf("实际股数管理费不得再乘杠杆，市值%v", marketValue)
		}
		if err := db.Transaction(func(tx *gorm.DB) error {
			if err := forceCloseAllPositions(tx, &customer, []system.TradePosition{p}, time.Now().Unix(), "实际股数自动强平测试"); err != nil {
				return err
			}
			return tx.Save(&customer).Error
		}); err != nil {
			t.Fatal(err)
		}
		checkBalance(11985.5, 0)
		if err := db.First(&p, p.ID).Error; err != nil {
			t.Fatal(err)
		}
		if p.PositionQty != 0 || p.Margin != 0 || p.Status != system.StatusDisabled {
			t.Fatal("自动强平未正确清仓")
		}
	})
}
