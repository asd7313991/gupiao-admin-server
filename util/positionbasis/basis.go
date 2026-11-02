package positionbasis

import (
	"math"
	"strings"

	"api-server/db/pgdb/system"
)

const (
	Legacy            uint    = 0
	Actual            uint    = 1
	FinancingLeverage float64 = 5
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

// 保留原担保金总额，只将超出买入基础本金的部分独立展示，不产生退款。
func MarginParts(p system.TradePosition) (base, supplemental float64) {
	if p.SupplementalMargin != nil {
		return Money(p.Margin - *p.SupplementalMargin), Money(*p.SupplementalMargin)
	}
	basis := p.QuantityBasis
	if IsActual(p) {
		basis = Actual
	}
	base = math.Min(p.Margin, BuyMargin(p.TotalCost, Leverage(p), basis))
	return Money(base), Money(p.Margin - base)
}

func SetSupplementalMargin(p *system.TradePosition, supplemental float64) {
	supplemental = Money(supplemental)
	p.SupplementalMargin = &supplemental
	p.BaseMargin = Money(p.Margin - supplemental)
}

func IsFinanced(p system.TradePosition) bool {
	return IsActual(p) && Leverage(p) == FinancingLeverage
}
