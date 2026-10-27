package positionbasis

import (
	"math"
	"strings"

	"api-server/db/pgdb/system"
)

const (
	Legacy uint = 0
	Actual uint = 1
)

func Leverage(p system.TradePosition) float64 {
	if p.Leverage >= 1 {
		return p.Leverage
	}
	return 1
}

func IsActual(p system.TradePosition) bool {
	return p.QuantityBasis == Actual || strings.HasSuffix(p.Symbol, ".HK")
}

func Multiplier(p system.TradePosition) float64 {
	if IsActual(p) {
		return 1
	}
	return Leverage(p)
}

func Money(value float64) float64 {
	return math.Round(value*100) / 100
}

func Values(p system.TradePosition, price float64) (marketValue, profit, rate float64) {
	multiplier := Multiplier(p)
	marketValue = Money(p.PositionQty * price * multiplier)
	profit = Money((p.PositionQty*price - p.TotalCost) * multiplier)
	if p.TotalCost > 0 {
		rate = profit / p.TotalCost * 100
	}
	return
}

func BuyMargin(amount, leverage float64, basis uint) float64 {
	if basis == Actual && leverage >= 1 {
		return Money(amount / leverage)
	}
	return Money(amount)
}
