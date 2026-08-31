package mobile

import "testing"

func TestDailyManagementFee(t *testing.T) {
	tests := []struct {
		marketValue float64
		rate        float64
		want        float64
	}{
		{marketValue: 10000, rate: 0.00028, want: 2.8},
		{marketValue: 295800, rate: 0.00028, want: 82.82},
		{marketValue: 0, rate: 0.00028, want: 0},
	}
	for _, test := range tests {
		if got := dailyManagementFee(test.marketValue, test.rate); got != test.want {
			t.Fatalf("dailyManagementFee(%v, %v) = %v, want %v", test.marketValue, test.rate, got, test.want)
		}
	}
}
