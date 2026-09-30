package trade

import (
	"testing"

	"api-server/db/pgdb/system"
)

func TestCalculateForceCloseSettlement(t *testing.T) {
	position := system.TradePosition{PositionQty: 100, TotalCost: 1000, Margin: 1000, Leverage: 5}

	amount, realized, settlement := calculateForceCloseSettlement(position, 11)
	if amount != 1100 || realized != 500 || settlement != 1500 {
		t.Fatalf("盈利强平结算错误：成交额=%v，盈亏=%v，结算额=%v", amount, realized, settlement)
	}

	amount, realized, settlement = calculateForceCloseSettlement(position, 9)
	if amount != 900 || realized != -500 || settlement != 500 {
		t.Fatalf("亏损强平结算错误：成交额=%v，盈亏=%v，结算额=%v", amount, realized, settlement)
	}
}
