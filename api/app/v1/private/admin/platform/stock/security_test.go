package stock

import "testing"

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
