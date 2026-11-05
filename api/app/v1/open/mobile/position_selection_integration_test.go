package mobile

import (
	"errors"
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"gorm.io/gorm"
)

func TestSellUsesOpenPositionAfterClosedHistory(t *testing.T) {
	db := financingTestDB(t)
	customer := system.Customer{Phone: "position-selection", Balance: 50000, Status: 1, FundStatus: 1, Verified: 1}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	security := system.StockSecurity{Code: "000001", Symbol: "000001.SZ", Name: "卖出持仓测试", LastPrice: 11.59, Status: 1}
	history := system.TradePosition{CustomerID: customer.ID, Symbol: security.Symbol, Currency: "CNY", Leverage: 1, Status: 2}
	if err := db.Create(&history).Error; err != nil {
		t.Fatal(err)
	}
	open := system.TradePosition{CustomerID: customer.ID, Symbol: security.Symbol, Currency: "CNY",
		PositionQty: 1000, AvailableQty: 1000, CostPrice: 11.8, TotalCost: 11800, Margin: 11800, Leverage: 1, Status: 1}
	if err := db.Create(&open).Error; err != nil {
		t.Fatal(err)
	}
	var shown []system.TradePosition
	if err := customerPositionQuery(db, customer.ID, "A").Find(&shown).Error; err != nil {
		t.Fatal(err)
	}
	if len(shown) != 1 || shown[0].ID != open.ID || shown[0].AvailableQty != 1000 {
		t.Fatal("复现前提：页面显示当前1000股可卖持仓")
	}
	var result orderResult
	var rejection string
	if err := db.Transaction(func(tx *gorm.DB) error {
		return executeMobileOrder(tx, &customer, security, orderInput{Direction: "卖出", Quantity: 1000},
			mobileTradeSettings{}, &result, &rejection)
	}); err != nil {
		t.Fatalf("页面可卖1000股却卖出失败：%s (%v)", rejection, err)
	}
	if result.PositionID != open.ID {
		t.Fatalf("卖出必须使用页面显示的非零持仓，不能使用历史空仓：%+v", result)
	}
	if err := db.First(&open, open.ID).Error; err != nil {
		t.Fatal(err)
	}
	if open.PositionQty != 0 || open.AvailableQty != 0 || customer.Balance != 61590 {
		t.Fatal("1000股按11.59卖出应释放11800本金并结算-210盈亏", open.PositionQty, customer.Balance)
	}
	var unchanged system.TradePosition
	if err := db.First(&unchanged, history.ID).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.PositionQty != 0 || unchanged.AvailableQty != 0 || unchanged.Status != history.Status {
		t.Fatal("卖出不得修改历史空仓")
	}
	var record system.TradeRecord
	if err := db.First(&record, result.RecordID).Error; err != nil || record.RealizedProfit == nil || *record.RealizedProfit != -210 {
		t.Fatalf("卖出盈亏错误：%+v err=%v", record, err)
	}
}

func TestPositionSelectionAcrossOrderLifecycle(t *testing.T) {
	for _, market := range []string{"A", "HK"} {
		t.Run(market, func(t *testing.T) {
			db := financingTestDB(t)
			now := time.Date(2026, 11, 5, 11, 0, 0, 0, time.FixedZone("HK", 8*3600))
			c := system.Customer{Phone: "selection-lifecycle", Balance: 50000, HKDBalance: 50000, Status: 1, FundStatus: 1, Verified: 1}
			if err := db.Create(&c).Error; err != nil {
				t.Fatal(err)
			}
			s := system.StockSecurity{Code: "000001", Symbol: "000001.SZ", Name: "持仓选择测试", LastPrice: 11.59, Status: 1}
			currency := "CNY"
			if market == "HK" {
				s.Code, s.Symbol, s.Market, currency = "02348", "02348.HK", "港股", "HKD"
			}
			if err := db.Create(&s).Error; err != nil {
				t.Fatal(err)
			}
			config := `{"trade":{"allDay":true},"hkTrade":{"enabled":true,"commission":0,"minCommission":0,"stampDuty":0,"regulatoryFee":0,"tradingFee":0,"settlementFee":0}}`
			if err := db.Create(&system.AppSystemSetting{Config: config}).Error; err != nil {
				t.Fatal(err)
			}
			settings, err := parseMobileTradeSettings(config)
			if err != nil {
				t.Fatal(err)
			}
			history := system.TradePosition{CustomerID: c.ID, Symbol: s.Symbol, Currency: currency, Leverage: 1, Status: 2}
			if err := db.Create(&history).Error; err != nil {
				t.Fatal(err)
			}
			p := system.TradePosition{CustomerID: c.ID, Symbol: s.Symbol, Currency: currency, PositionQty: 1000, AvailableQty: 1000,
				TotalCost: 11800, CostPrice: 11.8, Margin: 2360, Leverage: 5, QuantityBasis: 1, Status: 1}
			if err := db.Create(&p).Error; err != nil {
				t.Fatal(err)
			}
			laterHistory := history
			laterHistory.ID = 0
			if err := db.Create(&laterHistory).Error; err != nil {
				t.Fatal(err)
			}
			q := hongKongQuote{Code: s.Code, LastPrice: s.LastPrice, LotSize: 100, Kind: "GP", Currency: "HKD", QuoteAt: now.Unix(),
				Ask: []orderBookLevel{{Price: s.LastPrice, Volume: 100}}, Bid: []orderBookLevel{{Price: s.LastPrice, Volume: 100}}}
			check := func(qty, available float64) {
				t.Helper()
				if err := db.First(&p, p.ID).Error; err != nil {
					t.Fatal(err)
				}
				if p.PositionQty != qty || p.AvailableQty != available {
					t.Fatalf("必须扣减当前持仓，不能选历史空仓：qty=%v available=%v", p.PositionQty, p.AvailableQty)
				}
				for _, old := range []system.TradePosition{history, laterHistory} {
					var got system.TradePosition
					if err := db.First(&got, old.ID).Error; err != nil || got.PositionQty != 0 || got.AvailableQty != 0 || got.Leverage != 1 {
						t.Fatal("不应修改历史空仓", got, err)
					}
				}
			}
			sequence := 0
			runMarket := func(direction string, qty float64) error {
				sequence++
				return db.Transaction(func(tx *gorm.DB) error {
					if err := tx.First(&c, c.ID).Error; err != nil {
						return err
					}
					input := orderInput{Code: s.Code, Direction: direction, Quantity: qty}
					if market == "HK" {
						result, err := executeHongKongOrder(tx, &c, s, q,
							hongKongOrderInput{orderInput: input, RequestID: "selection-market-" + time.Unix(int64(sequence), 0).Format("150405")}, now, false)
						if err == nil && result.PositionID != p.ID {
							return errors.New("市价成交误选历史空仓")
						}
						return err
					}
					var result orderResult
					var rejection string
					err := executeMobileOrder(tx, &c, s, input, settings, &result, &rejection)
					if err == nil && result.PositionID != p.ID {
						return errors.New("市价成交误选历史空仓")
					}
					return err
				})
			}
			place := func(direction string, qty float64) (system.LimitOrder, error) {
				sequence++
				input := limitOrderInput{Code: s.Code, Direction: direction, Quantity: qty, LimitPrice: s.LastPrice}
				if market == "HK" {
					return placeHongKongLimitOrderDB(db, c.ID, s, q, hongKongLimitInput{limitOrderInput: input,
						RequestID: "selection-limit-" + time.Unix(int64(sequence), 0).Format("150405")}, now)
				}
				order, _, err := placeAStockLimitOrderDB(db, c.ID, s, input, settings)
				return order, err
			}
			fill := func(order system.LimitOrder) error {
				var filled bool
				var err error
				if market == "HK" {
					filled, err = matchHongKongLimitOrderDB(db, c.ID, order.ID, q, now)
				} else {
					err = db.Transaction(func(tx *gorm.DB) error {
						var err error
						filled, err = fillAStockLimitOrder(tx, &order, s, settings)
						return err
					})
				}
				if err == nil && !filled {
					return errors.New("限价委托未按有效价格成交")
				}
				return err
			}
			for _, direction := range []string{"买入", "卖出"} {
				if err := runMarket(direction, 100); err != nil {
					t.Fatal(direction, err)
				}
			}
			check(1000, 1000)
			buy, err := place("买入", 100)
			if err != nil {
				t.Fatal(err)
			}
			if err := fill(buy); err != nil {
				t.Fatal(err)
			}
			check(1100, 1100)
			for _, cancel := range []bool{true, false} {
				sell, err := place("卖出", 1000)
				if err != nil {
					t.Fatal(err)
				}
				check(1100, 100)
				if cancel {
					if market == "HK" {
						err = cancelHongKongLimitOrderDB(db, c.ID, sell.ID, now)
					} else {
						err = db.Transaction(func(tx *gorm.DB) error {
							if err := releaseLimitOrderFreeze(tx, &sell); err != nil {
								return err
							}
							return tx.Model(&sell).Update("status", limitOrderCancelled).Error
						})
					}
					if err != nil {
						t.Fatal(err)
					}
					check(1100, 1100)
				} else {
					if err := fill(sell); err != nil {
						t.Fatal(err)
					}
					check(100, 100)
				}
			}
			if _, err := place("卖出", 100); err != nil {
				t.Fatal(err)
			}
			check(100, 0)
			if err := runMarket("卖出", 100); err == nil {
				t.Fatal("真实持仓已冻结时仍须拒绝重复卖出，不能跳过可卖量为0的持仓")
			}
			check(100, 0)
			if err := db.Transaction(func(tx *gorm.DB) error {
				if err := tx.First(&c, c.ID).Error; err != nil {
					return err
				}
				return cancelCustomerPendingOrdersForMarket(tx, &c, c.ID, now.Unix(), market)
			}); err != nil {
				t.Fatal(err)
			}
			check(100, 100)
		})
	}
}

func TestTradingPositionSelectionRejectsAmbiguousAndDeleted(t *testing.T) {
	db := financingTestDB(t)
	p := system.TradePosition{CustomerID: 1, Symbol: "000001.SZ", PositionQty: 1000, AvailableQty: 1000, Leverage: 5, QuantityBasis: 1}
	if err := db.Create(&p).Error; err != nil {
		t.Fatal(err)
	}
	deleted := p
	deleted.ID = 0
	if err := db.Create(&deleted).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Delete(&deleted).Error; err != nil {
		t.Fatal(err)
	}
	check := func(customerID uint, symbol string, wantID uint, wantErr error) {
		t.Helper()
		if err := db.Transaction(func(tx *gorm.DB) error {
			got, err := lockTradingPosition(tx, customerID, symbol, false)
			if !errors.Is(err, wantErr) || got.ID != wantID {
				t.Fatalf("持仓选择错误：ID=%d err=%v，预期ID=%d err=%v", got.ID, err, wantID, wantErr)
			}
			return nil
		}); err != nil {
			t.Fatal(err)
		}
	}
	check(1, p.Symbol, p.ID, nil)
	check(2, p.Symbol, 0, gorm.ErrRecordNotFound)
	check(1, "600000.SH", 0, gorm.ErrRecordNotFound)
	second := p
	second.ID = 0
	if err := db.Create(&second).Error; err != nil {
		t.Fatal(err)
	}
	check(1, p.Symbol, 0, errMultipleOpenPositions)
	var rejection string
	var result orderResult
	err := db.Transaction(func(tx *gorm.DB) error {
		return executeMobileOrder(tx, &system.Customer{Model: gorm.Model{ID: 1}, Balance: 10000},
			system.StockSecurity{Symbol: p.Symbol, LastPrice: 10}, orderInput{Direction: "卖出", Quantity: 100},
			mobileTradeSettings{}, &result, &rejection)
	})
	if !errors.Is(err, errTradeRejected) || rejection != errMultipleOpenPositions.Error() {
		t.Fatal("多笔非零持仓须明确提示核账，不能错误显示可卖持仓不足", rejection, err)
	}
}
