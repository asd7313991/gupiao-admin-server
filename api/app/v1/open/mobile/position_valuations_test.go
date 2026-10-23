package mobile

import (
	"encoding/json"
	"strings"
	"testing"

	"api-server/db/pgdb/system"
	"api-server/util/positionquote"
	"gorm.io/gorm"
)

func TestPositionValuationsKeepUnpricedLedgerWithoutFakeAssets(t *testing.T) {
	items := []system.TradePosition{
		{Model: gorm.Model{ID: 1}, Symbol: "00700.HK", Currency: "HKD", PositionQty: 100, AvailableQty: 80, CostPrice: 380, TotalCost: 38000, Margin: 38000},
		{Model: gorm.Model{ID: 2}, Symbol: "00875.HK", Currency: "HKD", PositionQty: 2000, AvailableQty: 2000, CostPrice: 0.35, TotalCost: 700, Margin: 700},
	}
	rows, complete := availablePositionViews(items, map[string]positionquote.Quote{"00700.HK": {Price: 388, At: 1, Source: "tencent"}})
	if complete || len(rows) != 2 || rows[0].Valuation == nil || rows[0].Valuation.MarketValue != 38800 ||
		rows[1].Valuation != nil || rows[1].Holding.AvailableQty != 2000 || rows[1].CostPrice != 0.35 || rows[1].Error == "" {
		t.Fatalf("单只估值失败不得隐藏其他持仓、数量或编造市值：%+v", rows)
	}
	body, err := json.Marshal(rows[1])
	if err != nil || !strings.Contains(string(body), `"valuation":null`) || strings.Contains(string(body), "current_price") ||
		strings.Contains(string(body), "market_value") || strings.Contains(string(body), "profit_loss") {
		t.Fatalf("失败行须明确为空估值，不能以零冒充：%s %v", body, err)
	}
	rows, complete = availablePositionViews(nil, nil)
	if !complete || rows == nil || len(rows) != 0 {
		t.Fatal("真实空持仓必须返回空数组")
	}
}
