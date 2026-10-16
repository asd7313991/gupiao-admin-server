package stock

import (
	"encoding/json"
	"strings"
	"testing"

	"api-server/db/pgdb/system"
)

func TestClassifyBoard(t *testing.T) {
	tests := []struct {
		code   string
		market int
		want   string
	}{
		{code: "920093", market: 0, want: "北交所"},
		{code: "830799", market: 0, want: "北交所"},
		{code: "688185", market: 1, want: "科创板"},
		{code: "300839", market: 0, want: "创业板"},
		{code: "600519", market: 1, want: "主板"},
	}

	for _, test := range tests {
		t.Run(test.code, func(t *testing.T) {
			if got := classifyBoard(test.code, test.market); got != test.want {
				t.Fatalf("classifyBoard(%q, %d) = %q, want %q", test.code, test.market, got, test.want)
			}
		})
	}
}

func TestEastmoneyDateUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		data string
		want string
	}{
		{name: "数字日期", data: `20260901`, want: "2026-09-01"},
		{name: "字符串日期", data: `"20260901"`, want: "2026-09-01"},
		{name: "无效日期", data: `"-"`, want: ""},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got eastmoneyDate
			if err := json.Unmarshal([]byte(test.data), &got); err != nil {
				t.Fatalf("解析上市日期失败：%v", err)
			}
			if string(got) != test.want {
				t.Fatalf("上市日期 = %q, want %q", got, test.want)
			}
		})
	}
}

func TestFlexibleFloatUnmarshalJSON(t *testing.T) {
	tests := []struct {
		name string
		data string
		want float64
	}{
		{name: "数字价格", data: `6.78`, want: 6.78},
		{name: "字符串价格", data: `"6.78"`, want: 6.78},
		{name: "无报价", data: `"-"`, want: 0},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var got flexibleFloat
			if err := json.Unmarshal([]byte(test.data), &got); err != nil {
				t.Fatalf("解析行情价格失败：%v", err)
			}
			if float64(got) != test.want {
				t.Fatalf("行情价格 = %v, want %v", got, test.want)
			}
		})
	}
}

func TestParseTencentQuotes(t *testing.T) {
	fields := make([]string, 33)
	fields[0], fields[1], fields[2] = "51", "拉卡拉", "300773"
	fields[3], fields[4], fields[32] = "14.38", "14.38", "-2.38"
	body := "\n v_sz300773=\"" + strings.Join(fields, "~") + `";`
	quotes := parseTencentQuotes(body)
	quote, exists := quotes["sz300773"]
	if !exists {
		t.Fatal("未解析到拉卡拉行情")
	}
	if quote.Price != 14.38 || quote.ChangeRate != -2.38 {
		t.Fatalf("行情解析错误：%+v", quote)
	}
}

func TestTencentSymbol(t *testing.T) {
	tests := []struct {
		name     string
		security system.StockSecurity
		want     string
	}{
		{name: "深交所", security: system.StockSecurity{Code: "000584", Exchange: "深交所"}, want: "sz000584"},
		{name: "上交所", security: system.StockSecurity{Code: "600849", Exchange: "上交所"}, want: "sh600849"},
		{name: "北交所", security: system.StockSecurity{Code: "832317", Exchange: "北交所"}, want: "bj832317"},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := tencentSymbol(test.security); got != test.want {
				t.Fatalf("tencentSymbol(%+v) = %q, want %q", test.security, got, test.want)
			}
		})
	}
}

func TestUnavailableQuotesAreLabeled(t *testing.T) {
	securities := []system.StockSecurity{
		{Code: "300773", LastPrice: 14.38, Source: "eastmoney/tencent"},
		{Code: "920285", LastPrice: 0, Source: "eastmoney"},
	}

	for index := range securities {
		if securities[index].LastPrice <= 0 {
			securities[index].Source = "eastmoney/unavailable"
		}
	}

	if securities[0].Source != "eastmoney/tencent" {
		t.Fatalf("有效行情来源错误：%s", securities[0].Source)
	}
	if securities[1].Source != "eastmoney/unavailable" {
		t.Fatalf("无有效行情未标记：%s", securities[1].Source)
	}
}
