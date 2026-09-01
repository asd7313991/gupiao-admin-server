package mobile

import (
	"testing"
	"time"

	"api-server/db/pgdb/system"
)

func TestValidateSecurityTrade(t *testing.T) {
	settings := mobileTradeSettings{}
	settings.Limits.MainBoard = 0.08
	settings.Limits.GrowthBoard = 0.16
	settings.Limits.StarBoard = 0.16
	settings.Limits.BeijingBoard = 0.24
	settings.Limits.MinStarShares = 200
	now := time.Date(2026, time.September, 1, 10, 0, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60))

	tests := []struct {
		name       string
		security   system.StockSecurity
		direction  string
		quantity   float64
		wantReject bool
	}{
		{name: "上市当日不可买", security: system.StockSecurity{Name: "测试股份", Board: "主板", ListDate: "2026-09-01"}, direction: "买入", quantity: 100, wantReject: true},
		{name: "上市次日可买", security: system.StockSecurity{Name: "测试股份", Board: "主板", ListDate: "2026-08-31"}, direction: "买入", quantity: 100},
		{name: "N 前缀新股不可买", security: system.StockSecurity{Name: "N测试", Board: "主板"}, direction: "买入", quantity: 100, wantReject: true},
		{name: "ST 不可买", security: system.StockSecurity{Name: "ST测试", Board: "主板"}, direction: "买入", quantity: 100, wantReject: true},
		{name: "星号 ST 不可买", security: system.StockSecurity{Name: "*ST测试", Board: "主板"}, direction: "买入", quantity: 100, wantReject: true},
		{name: "SST 不可买", security: system.StockSecurity{Name: "SST测试", Board: "主板"}, direction: "买入", quantity: 100, wantReject: true},
		{name: "主板等于八点可买", security: system.StockSecurity{Name: "测试股份", Board: "主板", ChangeRate: 8}, direction: "买入", quantity: 100},
		{name: "主板超过八点不可买", security: system.StockSecurity{Name: "测试股份", Board: "主板", ChangeRate: 8.01}, direction: "买入", quantity: 100, wantReject: true},
		{name: "创业板等于十六点可买", security: system.StockSecurity{Name: "测试股份", Board: "创业板", ChangeRate: 16}, direction: "买入", quantity: 100},
		{name: "创业板超过十六点不可买", security: system.StockSecurity{Name: "测试股份", Board: "创业板", ChangeRate: 16.01}, direction: "买入", quantity: 100, wantReject: true},
		{name: "科创板等于十六点可买", security: system.StockSecurity{Name: "测试股份", Board: "科创板", ChangeRate: 16}, direction: "买入", quantity: 200},
		{name: "科创板超过十六点不可买", security: system.StockSecurity{Name: "测试股份", Board: "科创板", ChangeRate: 16.01}, direction: "买入", quantity: 200, wantReject: true},
		{name: "北交所等于二十四点可买", security: system.StockSecurity{Name: "测试股份", Board: "北交所", ChangeRate: 24}, direction: "买入", quantity: 100},
		{name: "北交所超过二十四点不可买", security: system.StockSecurity{Name: "测试股份", Board: "北交所", ChangeRate: 24.01}, direction: "买入", quantity: 100, wantReject: true},
		{name: "科创板不足二百股不可买", security: system.StockSecurity{Name: "测试股份", Board: "科创板"}, direction: "买入", quantity: 199, wantReject: true},
		{name: "科创板二百股可买", security: system.StockSecurity{Name: "测试股份", Board: "科创板"}, direction: "买入", quantity: 200},
		{name: "卖出不受买入限制", security: system.StockSecurity{Name: "SST测试", Board: "科创板", ListDate: "2026-09-01", ChangeRate: 20}, direction: "卖出", quantity: 1},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			message := validateSecurityTradeAt(test.security, orderInput{Direction: test.direction, Quantity: test.quantity}, settings, now)
			if gotReject := message != ""; gotReject != test.wantReject {
				t.Fatalf("校验结果不符：message=%q, wantReject=%v", message, test.wantReject)
			}
		})
	}
}
