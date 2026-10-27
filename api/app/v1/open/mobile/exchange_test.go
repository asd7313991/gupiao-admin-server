package mobile

import (
	"math"
	"testing"
)

func TestExchangeReceived(t *testing.T) {
	for _, tc := range []struct {
		name               string
		from               string
		amount, rate, want float64
		bad                bool
	}{
		{"人民币兑港币", "CNY", 100, 0.8516, 117.42, false},
		{"港币兑人民币", "HKD", 100, 0.8516, 85.16, false},
		{"往返向下取分", "HKD", 117.42, 0.8516, 99.99, false},
		{"一分钱", "CNY", 0.01, 0.89, 0.01, false},
		{"舍入不增资", "HKD", 0.01, 0.89, 0, true},
		{"二进制浮点分界", "HKD", 0.29, 1, 0.29, false},
		{"最大兑出金额", "CNY", 5_000_000, 0.89, 5617977.52, false},
		{"金额超过上限", "CNY", 5_000_000.01, 0.89, 0, true},
		{"三位小数", "CNY", 1.001, 0.89, 0, true},
		{"负数", "CNY", -1, 0.89, 0, true},
		{"零金额", "CNY", 0, 0.89, 0, true},
		{"金额非数", "CNY", math.NaN(), 0.89, 0, true},
		{"无限金额", "CNY", math.Inf(1), 0.89, 0, true},
		{"零汇率", "CNY", 100, 0, 0, true},
		{"负汇率", "CNY", 100, -0.89, 0, true},
		{"非数汇率", "CNY", 100, math.NaN(), 0, true},
		{"无限汇率", "CNY", 100, math.Inf(1), 0, true},
		{"汇率过小", "CNY", 100, 1e-10, 0, true},
		{"汇率精度", "CNY", 100, 0.123456789, 0, true},
		{"错误币种", "USD", 100, 0.89, 0, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := exchangeReceived(tc.amount, tc.rate, tc.from)
			if (err != nil) != tc.bad || !tc.bad && got != tc.want {
				t.Fatalf("got=%v err=%v want=%v bad=%v", got, err, tc.want, tc.bad)
			}
		})
	}
}
