package mobile

import (
	"testing"

	"api-server/db/pgdb/system"
)

func TestMatchPriceFromOrderBook(t *testing.T) {
	tests := []struct {
		name      string
		order     system.LimitOrder
		book      orderBookView
		wantPrice float64
		wantMatch bool
	}{
		{
			name:      "buy limit-up order remains pending without asks",
			order:     system.LimitOrder{Direction: "买入", LimitPrice: 10.80},
			book:      orderBookView{Ask: []orderBookLevel{{Price: 0, Volume: 0}}},
			wantMatch: false,
		},
		{
			name:      "buy order fills at executable ask",
			order:     system.LimitOrder{Direction: "买入", LimitPrice: 10.80},
			book:      orderBookView{Ask: []orderBookLevel{{Price: 10.79, Volume: 100}}},
			wantPrice: 10.79,
			wantMatch: true,
		},
		{
			name:      "sell limit-down order remains pending without bids",
			order:     system.LimitOrder{Direction: "卖出", LimitPrice: 9.20},
			book:      orderBookView{Bid: []orderBookLevel{{Price: 0, Volume: 0}}},
			wantMatch: false,
		},
		{
			name:      "sell order fills at executable bid",
			order:     system.LimitOrder{Direction: "卖出", LimitPrice: 9.20},
			book:      orderBookView{Bid: []orderBookLevel{{Price: 9.21, Volume: 100}}},
			wantPrice: 9.21,
			wantMatch: true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			price, matched := matchPriceFromOrderBook(test.order, test.book)
			if matched != test.wantMatch || price != test.wantPrice {
				t.Fatalf("matchPriceFromOrderBook() = (%v, %v), want (%v, %v)", price, matched, test.wantPrice, test.wantMatch)
			}
		})
	}
}
