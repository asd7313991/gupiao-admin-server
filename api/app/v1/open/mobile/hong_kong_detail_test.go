package mobile

import (
	"context"
	"encoding/json"
	"math"
	"os"
	"strings"
	"testing"
	"time"

	"api-server/db/pgdb/system"
)

func hongKongDetailFixture() (system.StockSecurity, time.Time, []string) {
	now := time.Date(2026, 10, 27, 11, 41, 0, 0, time.FixedZone("HK", 8*3600))
	item := system.StockSecurity{Code: "01810", Symbol: "01810.HK", Name: "小米集团－Ｗ", Market: "港股", Exchange: "港交所", LastPrice: 999}
	fields := make([]string, 78)
	for i := range fields {
		fields[i] = "0"
	}
	fields[1], fields[2], fields[3], fields[4] = "小米集团-W", "01810", "25.800", "26.900"
	fields[5], fields[6], fields[30] = "26.700", "68839618", "2026/10/27 11:40:53"
	fields[31], fields[32], fields[33], fields[34], fields[37] = "-1.100", "-4.09", "27.040", "25.640", "1790000000"
	fields[9], fields[19] = "25.780", "25.800"
	fields[60], fields[63], fields[75] = "200", "GP", "HKD"
	return item, now, fields
}

func hongKongDetailBody(alias string, fields []string) string {
	return `v_` + alias + `="` + strings.Join(fields, "~") + `";`
}

func TestHongKongDetailSnapshot(t *testing.T) {
	item, now, fields := hongKongDetailFixture()
	older := append([]string(nil), fields...)
	older[30] = "2026/10/27 11:25:53"
	older[3], older[31], older[32] = "25.780", "-1.120", "-4.16"
	view, _, err := parseHongKongDetailSnapshot(hongKongDetailBody("hk01810", older)+hongKongDetailBody("r_hk01810", fields), item, now)
	if err != nil {
		t.Fatal(err)
	}
	if !view.SnapshotValidated || view.LastPrice != 25.8 || view.PrevClose != 26.9 ||
		view.Open != 26.7 || view.High != 27.04 || view.Low != 25.64 || view.ChangeRate != -4.09 ||
		view.Volume != 68839618 || view.Amount != 1790000000 || view.Currency != "HKD" || view.LotSize != 200 ||
		view.QuoteAt != now.Add(-7*time.Second).Unix() || quoteSymbol(item) != "hk01810" {
		t.Fatalf("港股必须整份择新，股数不乘手数，港币金额不乘10000：%+v", view)
	}
	book, err := parseHongKongOrderBook(hongKongDetailBody("r_hk01810", fields), item, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(book.Bid) != 1 || len(book.Ask) != 1 || book.Bid[0].Price != 25.78 ||
		book.Bid[0].VolumeAvailable == nil || *book.Bid[0].VolumeAvailable || book.LimitUp != 0 || book.LimitDown != 0 ||
		book.Source != "tencent" || book.QuoteAt != view.QuoteAt {
		t.Fatalf("不得补造五档、挂单数量或A股涨跌停：%+v", book)
	}
	invalid := append([]string(nil), fields...)
	invalid[4] = "30"
	fallback, _, err := parseHongKongDetailSnapshot(hongKongDetailBody("r_hk01810", invalid)+hongKongDetailBody("hk01810", older), item, now)
	if err != nil || fallback.LastPrice != 25.78 || fallback.PrevClose != 26.9 || fallback.QuoteAt != now.Add(-907*time.Second).Unix() {
		t.Fatalf("无效候选不能污染另一份完整有效快照：%+v %v", fallback, err)
	}
}

func TestHongKongDetailRejectsInvalidSnapshots(t *testing.T) {
	item, now, fields := hongKongDetailFixture()
	for _, tt := range []struct {
		index int
		value string
	}{{2, "00700"}, {3, "NaN"}, {4, "0"}, {5, "30"}, {6, "-1"}, {30, "2026/10/27 11:43:00"},
		{30, "2026/10/27 10:00:00"}, {31, "1.1"}, {32, "4.09"}, {33, "25"}, {37, "Inf"}, {60, "0"}, {75, ""}} {
		bad := append([]string(nil), fields...)
		bad[tt.index] = tt.value
		if _, _, err := parseHongKongDetailSnapshot(hongKongDetailBody("r_hk01810", bad), item, now); err == nil {
			t.Fatalf("应拒绝字段%d=%q", tt.index, tt.value)
		}
	}
	body := hongKongDetailBody("r_hk01810", fields)
	for _, bad := range []string{body + body, hongKongDetailBody("r_hk00700", fields), hongKongDetailBody("r_hk01810", fields[:35])} {
		if _, _, err := parseHongKongDetailSnapshot(bad, item, now); err == nil {
			t.Fatal("应拒绝重复、错误证券和缺失字段")
		}
	}
	for _, badVolume := range []string{"-1", "NaN"} {
		bad := append([]string(nil), fields...)
		bad[10] = badVolume
		if _, err := parseHongKongOrderBook(hongKongDetailBody("r_hk01810", bad), item, now); err == nil {
			t.Fatal("无效盘口不能伪装成零量档位")
		}
	}
}

func TestHongKongMinuteUnitsAndReferences(t *testing.T) {
	days := []tencentMinuteDay{
		{Date: "20261027", Prec: "26.9", Data: []string{"0930 26.7 200 5340", "0931 26.8 500 13380"}},
		{Date: "20261026", Prec: "26.8", Data: []string{"0930 26.9 100 2690"}},
	}
	points, err := parseHongKongMinuteDays(days, true)
	if err != nil {
		t.Fatal(err)
	}
	if len(points) != 3 || points[0].Time != "2026-10-26 09:30" || points[0].PrevClose != 26.8 ||
		points[2].Time != "2026-10-27 09:31" || points[2].Volume != 300 || points[2].Amount != 8040 ||
		math.Abs(points[2].AveragePrice-26.76) > .000001 || points[2].PrevClose != 26.9 {
		t.Fatalf("必须按股/港币差分，每日重置累计均价并按时间升序：%+v", points)
	}
	for _, lines := range [][]string{
		{"0930 26.7 200"}, {"0960 26.7 200 5340"}, {"0930 NaN 200 5340"},
		{"0930 26.7 200 5340", "0931 26.8 100 8040"},
		{"0931 26.8 100 8040", "0930 26.7 200 5340"},
		{"0930 26.7 0 5340"}, {"0930 26.7 200 NaN"},
	} {
		if _, err := parseHongKongMinuteDays([]tencentMinuteDay{{Date: "20261027", Data: lines}}, false); err == nil {
			t.Fatalf("应拒绝无效分时：%v", lines)
		}
	}
	if _, err := parseHongKongMinuteDays([]tencentMinuteDay{{Date: "20261027", Data: days[0].Data}}, true); err == nil {
		t.Fatal("五日图不能将今天昨收套用到历史日期")
	}
}

func TestHongKongKLineRows(t *testing.T) {
	var rows [][]json.RawMessage
	if err := json.Unmarshal([]byte(`[["2026-10-27","26.7","25.8","27.04","25.64","68839618",{"cqr":"2026-10-27"}]]`), &rows); err != nil {
		t.Fatal(err)
	}
	points, err := parseHongKongKLineRows(rows)
	if err != nil || len(points) != 1 || points[0].Volume != 68839618 || points[0].AmountAvailable == nil || *points[0].AmountAvailable {
		t.Fatalf("公司行动列不能当成交额，成交量必须保留股数：%+v %v", points, err)
	}
	rows[0][3] = json.RawMessage(`"25"`)
	if _, err := parseHongKongKLineRows(rows); err == nil {
		t.Fatal("应拒绝高低价不一致的港股K线")
	}
}

func TestLiveHongKongDetail(t *testing.T) {
	if os.Getenv("DETAIL_QUOTE_LIVE_TEST") != "1" {
		t.Skip("未启用公开港股行情只读验证")
	}
	for _, code := range []string{"01810", "00700", "09988", "00005"} {
		t.Run(code, func(t *testing.T) {
			item := system.StockSecurity{Code: code, Symbol: code + ".HK", Market: "港股", Exchange: "港交所"}
			body, err := fetchTencentQuoteBody(context.Background(), item)
			if err != nil {
				t.Fatal(err)
			}
			view, _, err := parseHongKongDetailSnapshot(body, item, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			book, err := parseHongKongOrderBook(body, item, time.Now())
			if err != nil {
				t.Fatal(err)
			}
			t.Logf("价格=%.3f 昨收=%.3f 量=%.0f股 金额=%.2f %s 可用买卖档=%d/%d",
				view.LastPrice, view.PrevClose, view.Volume, view.Amount, view.Currency, len(book.Bid), len(book.Ask))
			for _, five := range []bool{false, true} {
				points, err := fetchMinuteLines(item, five)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("五日=%t 点数=%d 最后=%s", five, len(points), points[len(points)-1].Time)
			}
			for _, period := range []string{"day", "week", "month"} {
				points, _, err := fetchKLines(item, period, 30)
				if err != nil {
					t.Fatal(err)
				}
				t.Logf("周期=%s 点数=%d 最后=%s", period, len(points), points[len(points)-1].Time)
			}
		})
	}
}
