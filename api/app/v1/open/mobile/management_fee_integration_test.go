package mobile

import (
	"math"
	"os"
	"testing"
	"time"

	"gorm.io/gorm"

	"api-server/config"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

func TestManagementFeeForcesCloseWhenBalanceIsInsufficient(t *testing.T) {
	dsn := os.Getenv("MANAGEMENT_FEE_TEST_DSN")
	if dsn == "" {
		t.Skip("MANAGEMENT_FEE_TEST_DSN is not set")
	}
	config.PgsqlDSN = dsn
	db := pgdb.GetClient()
	if err := db.AutoMigrate(&system.DailyManagementFee{}); err != nil {
		t.Fatal(err)
	}
	var security system.StockSecurity
	if err := db.Where("status = ? AND last_price > 0", system.StatusEnabled).First(&security).Error; err != nil {
		t.Fatal(err)
	}
	customer := system.Customer{Phone: "19928000002", Name: "ManagementFeeProbe", IDCard: "110101199001010011", Balance: 0, Status: system.StatusEnabled, FundStatus: system.StatusEnabled, Verified: system.StatusEnabled}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	defer cleanupManagementFeeProbe(db, customer.ID)
	amount := roundMoney(security.LastPrice * 100)
	position := system.TradePosition{CustomerID: customer.ID, Symbol: security.Symbol, StockName: security.Name, Currency: "CNY", PositionQty: 100, AvailableQty: 100, CurrentPrice: security.LastPrice, CostPrice: security.LastPrice, TotalCost: amount, Margin: roundMoney(amount / 5), Leverage: 5, MarketValue: amount, Status: system.StatusEnabled, BuyAt: time.Now().Unix()}
	if err := db.Create(&position).Error; err != nil {
		t.Fatal(err)
	}
	chargeDate := "2099-12-31"
	forced, err := processCustomerManagementFee(customer.ID, chargeDate, 0.00028, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if !forced {
		t.Fatal("expected forced close")
	}
	if err := db.First(&position, position.ID).Error; err != nil {
		t.Fatal(err)
	}
	if position.PositionQty != 0 || position.Status != system.StatusDisabled {
		t.Fatalf("position was not fully closed: qty=%v status=%v", position.PositionQty, position.Status)
	}
	if err := db.First(&customer, customer.ID).Error; err != nil {
		t.Fatal(err)
	}
	wantBalance := roundMoney(amount/5 - dailyManagementFee(amount, 0.00028))
	if math.Abs(customer.Balance-wantBalance) > 0.001 {
		t.Fatalf("balance=%v want=%v", customer.Balance, wantBalance)
	}
	if _, err := processCustomerManagementFee(customer.ID, chargeDate, 0.00028, time.Now()); err != gorm.ErrDuplicatedKey {
		t.Fatalf("second charge should be rejected, got %v", err)
	}
}

func cleanupManagementFeeProbe(db *gorm.DB, customerID uint) {
	db.Unscoped().Where("customer_id = ?", customerID).Delete(&system.DailyManagementFee{})
	db.Unscoped().Where("customer_id = ?", customerID).Delete(&system.CustomerFundRecord{})
	db.Unscoped().Where("customer_id = ?", customerID).Delete(&system.TradeRecord{})
	db.Unscoped().Where("customer_id = ?", customerID).Delete(&system.TradePosition{})
	db.Unscoped().Delete(&system.Customer{}, customerID)
}
