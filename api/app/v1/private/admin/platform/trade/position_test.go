package trade

import (
	"strings"
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

func TestActualPositionSettlementAndValuation(t *testing.T) {
	position := system.TradePosition{QuantityBasis: 1, PositionQty: 1000, TotalCost: 10000, Margin: 2000, Leverage: 5}
	amount, realized, settlement := calculateForceCloseSettlement(position, 11)
	if amount != 11000 || realized != 1000 || settlement != 3000 {
		t.Fatalf("实际股数强平%v/%v/%v", amount, realized, settlement)
	}
	refreshPositionMarketValues(&position, 11)
	if position.MarketValue != 11000 || position.ProfitLoss != 1000 || position.ProfitRate != 10 {
		t.Fatalf("后台与客户端口径须一致%+v", position)
	}
}

func TestRefreshPositionMarketValues(t *testing.T) {
	position := system.TradePosition{PositionQty: 40000, CostPrice: 6.78, TotalCost: 271200, Leverage: 5}

	refreshPositionMarketValues(&position, 6.31)

	if position.CurrentPrice != 6.31 || position.MarketValue != 1262000 || position.ProfitLoss != -94000 || position.ProfitRate != -34.66 {
		t.Fatalf("实时持仓计算错误：%+v", position)
	}
}

func TestParseLivePositionQuotes(t *testing.T) {
	fields := make([]string, 4)
	fields[3] = "6.78"
	quotes := parseLivePositionQuotes(`v_sh600802="` + strings.Join(fields, "~") + `";`)
	if quotes["sh600802"] != 6.78 {
		t.Fatalf("实时行情解析错误：%v", quotes)
	}

}

func TestHongKongPositionQuoteSymbol(t *testing.T) {
	if got := positionQuoteSymbol("03774.HK"); got != "hk03774" {
		t.Fatalf("港股报价代码=%s", got)
	}
	position := system.TradePosition{PositionQty: 1000, TotalCost: 35500, Margin: 35500, Leverage: 1, Currency: "HKD"}
	amount, realized, settlement := calculateForceCloseSettlement(position, 36)
	if amount != 36000 || realized != 500 || settlement != 36000 {
		t.Fatalf("港币全额结算 amount=%v realized=%v settlement=%v", amount, realized, settlement)
	}
}
