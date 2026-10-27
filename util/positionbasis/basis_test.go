package positionbasis

import (
	"math"
	"testing"

	"api-server/db/pgdb/system"
)

func TestActualAndLegacyValues(t *testing.T) {
	for _, tc := range []struct {
		name                string
		p                   system.TradePosition
		value, profit, rate float64
	}{
		{"实际1000股五倍", system.TradePosition{QuantityBasis: Actual, PositionQty: 1000, TotalCost: 10000, Leverage: 5}, 11000, 1000, 10},
		{"旧1000基础股五倍", system.TradePosition{PositionQty: 1000, TotalCost: 10000, Leverage: 5}, 55000, 5000, 50},
		{"实际一倍", system.TradePosition{QuantityBasis: Actual, PositionQty: 1000, TotalCost: 10000, Leverage: 1}, 11000, 1000, 10},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v, p, r := Values(tc.p, 11)
			if v != tc.value || p != tc.profit || r != tc.rate {
				t.Fatalf("市值%v 盈亏%v 收益率%v", v, p, r)
			}
		})
	}
	if BuyMargin(10000, 5, Actual) != 2000 || BuyMargin(10000, 5, Legacy) != 10000 {
		t.Fatal("新旧本金口径不能混用")
	}
}

func TestConversionPreservesExposure(t *testing.T) {
	old := system.TradePosition{PositionQty: 300, TotalCost: 6327, Margin: 6327, Leverage: 5}
	converted := old
	converted.QuantityBasis = Actual
	converted.PositionQty *= 5
	converted.TotalCost *= 5
	v, p, r := Values(old, 21.69)
	nv, np, nr := Values(converted, 21.69)
	if v != 32535 || p != 900 || nv != v || np != p || math.Abs(nr-r/5) > 1e-10 || converted.Margin != old.Margin {
		t.Fatalf("转换不得改变敞口或资金权益 old=%v/%v/%v new=%v/%v/%v", v, p, r, nv, np, nr)
	}
}
