package mobile

import (
	"math"
	"os"
	"testing"
	"time"

	"api-server/config"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

func TestMarginCallsSupplementAndForceClose(t *testing.T) {
	dsn := os.Getenv("MANAGEMENT_FEE_TEST_DSN")
	if dsn == "" {
		t.Skip("MANAGEMENT_FEE_TEST_DSN is not set")
	}
	config.PgsqlDSN = dsn
	db := pgdb.GetClient()
	var security system.StockSecurity
	if err := db.Where("status = ? AND last_price > 0", system.StatusEnabled).First(&security).Error; err != nil {
		t.Fatal(err)
	}

	t.Run("supplements every crossed point once", func(t *testing.T) {
		customer := system.Customer{Phone: "19928000003", Name: "MarginCallProbe", IDCard: "110101199001010012", Balance: 10000, Status: system.StatusEnabled, FundStatus: system.StatusEnabled, Verified: system.StatusEnabled}
		if err := db.Create(&customer).Error; err != nil {
			t.Fatal(err)
		}
		defer cleanupManagementFeeProbe(db, customer.ID)
		position := marginCallTestPosition(customer.ID, security, 18.2)
		if err := db.Create(&position).Error; err != nil {
			t.Fatal(err)
		}
		beforeBalance, beforeMargin := customer.Balance, position.Margin
		count, forced, err := processCustomerMarginCalls(customer.ID, 16, 0.005, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if forced || count != 3 {
			t.Fatalf("count=%d forced=%v, want count=3 forced=false", count, forced)
		}
		if err := db.First(&customer, customer.ID).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.First(&position, position.ID).Error; err != nil {
			t.Fatal(err)
		}
		each := roundMoney(position.MarketValue * 0.005)
		if math.Abs(customer.Balance-(beforeBalance-each*3)) > 0.001 || math.Abs(position.Margin-(beforeMargin+each*3)) > 0.001 || position.MarginCallLevel != 18 {
			t.Fatalf("unexpected supplement result: balance=%v margin=%v level=%d", customer.Balance, position.Margin, position.MarginCallLevel)
		}
		var flowCount int64
		db.Model(&system.CustomerFundRecord{}).Where("customer_id = ? AND type = ?", customer.ID, "风险补仓").Count(&flowCount)
		if flowCount != 3 {
			t.Fatalf("flow count=%d, want 3", flowCount)
		}
		count, forced, err = processCustomerMarginCalls(customer.ID, 16, 0.005, time.Now())
		if err != nil || forced || count != 0 {
			t.Fatalf("repeat call count=%d forced=%v err=%v", count, forced, err)
		}
	})

	t.Run("forces all positions when cash is insufficient", func(t *testing.T) {
		customer := system.Customer{Phone: "19928000004", Name: "MarginForceProbe", IDCard: "110101199001010013", Balance: 0, Status: system.StatusEnabled, FundStatus: system.StatusEnabled, Verified: system.StatusEnabled}
		if err := db.Create(&customer).Error; err != nil {
			t.Fatal(err)
		}
		defer cleanupManagementFeeProbe(db, customer.ID)
		first := marginCallTestPosition(customer.ID, security, 17.2)
		second := marginCallTestPosition(customer.ID, security, 0)
		second.Symbol = security.Symbol + "-SECOND"
		if err := db.Create(&first).Error; err != nil {
			t.Fatal(err)
		}
		if err := db.Create(&second).Error; err != nil {
			t.Fatal(err)
		}
		count, forced, err := processCustomerMarginCalls(customer.ID, 16, 0.005, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		if !forced || count != 0 {
			t.Fatalf("count=%d forced=%v, want count=0 forced=true", count, forced)
		}
		var openPositions int64
		db.Model(&system.TradePosition{}).Where("customer_id = ? AND position_qty > 0", customer.ID).Count(&openPositions)
		if openPositions != 0 {
			t.Fatalf("open positions=%d, want 0", openPositions)
		}
		var forcedRecords int64
		db.Model(&system.TradeRecord{}).Where("customer_id = ? AND remark = ?", customer.ID, "风险补仓资金不足，系统全部平仓").Count(&forcedRecords)
		if forcedRecords != 2 {
			t.Fatalf("forced records=%d, want 2", forcedRecords)
		}
	})
}

func marginCallTestPosition(customerID uint, security system.StockSecurity, lossRate float64) system.TradePosition {
	marketValue := roundMoney(security.LastPrice * 100)
	totalCost := marketValue
	if lossRate > 0 {
		totalCost = roundMoney(marketValue / (1 - lossRate/100))
	}
	return system.TradePosition{CustomerID: customerID, Symbol: security.Symbol, StockName: security.Name, Currency: "CNY", PositionQty: 100, AvailableQty: 100, CurrentPrice: security.LastPrice, CostPrice: totalCost / 100, TotalCost: totalCost, Margin: roundMoney(totalCost / 5), Leverage: 5, MarketValue: marketValue, ProfitLoss: marketValue - totalCost, Status: system.StatusEnabled, BuyAt: time.Now().Unix()}
}
