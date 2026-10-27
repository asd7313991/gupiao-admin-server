package mobile

import (
	"context"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"api-server/db/pgdb/system"
)

func detailSnapshotFixture() (system.StockSecurity, time.Time, []string) {
	now := time.Date(2026, 10, 27, 10, 10, 0, 0, time.FixedZone("UTC+8", 8*3600))
	item := system.StockSecurity{Code: "002384", Symbol: "002384.SZ", Name: "东山精密", Market: "A股", Exchange: "深交所",
		LastPrice: 999, Source: "eastmoney", QuoteAt: now.Add(-time.Hour).Unix()}
	fields := make([]string, 49)
	for i := range fields {
		fields[i] = "0"
	}
	fields[1], fields[2], fields[3], fields[4] = "东山精密", "002384", "107.55", "105.43"
	fields[5], fields[6], fields[30] = "108.59", "190600", "20261027100958"
	fields[31], fields[32], fields[33], fields[34] = "2.12", "2.01", "109.30", "107.00"
	fields[37], fields[38] = "206200", "1.25"
	return item, now, fields
}

func TestAStockDetailSnapshot(t *testing.T) {
	item, now, fields := detailSnapshotFixture()
	body := `v_sz002384="` + strings.Join(fields, "~") + `";`
	view, err := parseAStockDetailSnapshot(body, item, now)
	if err != nil {
		t.Fatal(err)
	}
	if !view.SnapshotValidated || view.LastPrice != 107.55 || view.PrevClose != 105.43 || view.ChangeRate != 2.01 ||
		view.Open != 108.59 || view.High != 109.30 || view.Low != 107 || view.Volume != 190600 || view.Amount != 2062000000 ||
		view.Source != "tencent" || view.QuoteAt != now.Add(-2*time.Second).Unix() || view.UpdatedAt != now.Unix() {
		t.Fatalf("必须使用完整同源快照而非数据库、日K或分时拼接：%+v", view)
	}
	if math.Abs((view.LastPrice-view.PrevClose)/view.PrevClose*100-view.ChangeRate) > .005 {
		t.Fatal("价格、昨收和涨幅不一致")
	}
}

func TestAStockDetailRejectsInvalidSnapshots(t *testing.T) {
	item, now, fields := detailSnapshotFixture()
	tests := []struct {
		name  string
		index int
		value string
	}{
		{"昨收错用旧日K", 4, "113.51"},
		{"涨幅不一致", 32, "-5.88"},
		{"涨跌额不一致", 31, "-5.96"},
		{"错误代码", 2, "002242"},
		{"时间过期", 30, "20261027090000"},
		{"时间超前", 30, "20261027101200"},
		{"价格非有限数", 3, "NaN"},
		{"最高无效", 33, "Infinity"},
		{"现价超出最高", 33, "107.50"},
		{"最低高于今开", 34, "108.60"},
		{"空昨收", 4, ""},
		{"负成交量", 6, "-1"},
		{"缺成交额", 37, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			bad := append([]string(nil), fields...)
			bad[tt.index] = tt.value
			if _, err := parseAStockDetailSnapshot(`v_sz002384="`+strings.Join(bad, "~")+`";`, item, now); err == nil {
				t.Fatal("不得用零值或旧快照冒充有效行情")
			}
		})
	}
	body := `v_sz002384="` + strings.Join(fields, "~") + `";`
	for _, bad := range []string{body + body, `v_sh002384="` + strings.Join(fields, "~") + `";`, `v_sz002384="` + strings.Join(fields[:33], "~") + `";`} {
		if _, err := parseAStockDetailSnapshot(bad, item, now); err == nil {
			t.Fatal("重复、错误市场或缺少完整字段必须拒绝")
		}
	}
}

func TestAStockDetailNoTrades(t *testing.T) {
	item, now, fields := detailSnapshotFixture()
	fields[3], fields[4] = "105.43", "105.43"
	for _, i := range []int{5, 6, 31, 32, 33, 34, 37, 38} {
		fields[i] = "0"
	}
	view, err := parseAStockDetailSnapshot(`v_sz002384="`+strings.Join(fields, "~")+`";`, item, now)
	if err != nil {
		t.Fatal(err)
	}
	if view.Open != 0 || view.High != 0 || view.Low != 0 || view.LastPrice != view.PrevClose {
		t.Fatal("无成交时不能伪造开高低")
	}
}

func TestLiveAStockDetailSnapshot(t *testing.T) {
	if os.Getenv("DETAIL_QUOTE_LIVE_TEST") != "1" {
		t.Skip("未启用公开行情只读验证")
	}
	item := system.StockSecurity{Code: "002384", Symbol: "002384.SZ", Name: "东山精密", Market: "A股", Exchange: "深交所"}
	body, err := fetchTencentQuoteBody(context.Background(), item)
	if err != nil {
		t.Fatal(err)
	}
	view, err := parseAStockDetailSnapshot(body, item, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("来源=%s 报价时间=%s 现价=%.2f 昨收=%.2f 涨幅=%.2f%% 今开=%.2f 最高=%.2f 最低=%.2f",
		view.Source, time.Unix(view.QuoteAt, 0).Format(time.RFC3339), view.LastPrice, view.PrevClose, view.ChangeRate, view.Open, view.High, view.Low)
}
