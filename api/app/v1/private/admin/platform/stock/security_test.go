package stock

import (
	"encoding/json"
	"testing"
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
