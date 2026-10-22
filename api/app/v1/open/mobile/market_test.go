package mobile

import (
	"encoding/json"
	"testing"

	"api-server/db/pgdb/system"
)

func TestSecurityViewPreservesHongKongQuote(t *testing.T) {
	item := system.StockSecurity{
		Code: "00700", Symbol: "00700.HK", Market: "港股", Exchange: "港交所",
		LastPrice: 410.123, Volume: 1234, Amount: 506400.5,
		Source: "sina/tencent", QuoteAt: 1792476000, Status: system.StatusDisabled,
	}
	view := toSecurityView(item)
	if view.Code != "00700" || view.Symbol != "00700.HK" || view.LastPrice != 410.123 ||
		view.Volume != 1234 || view.Amount != 506400.5 || view.Source != item.Source || view.QuoteAt != item.QuoteAt {
		t.Fatalf("港股只读行情丢失代码、精度、单位或来源时间：%+v", view)
	}
	data, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	if decoded["quote_at"] != float64(item.QuoteAt) || decoded["source"] != item.Source {
		t.Fatalf("报价时间或来源未使用客户端字段名：%s", data)
	}
}
