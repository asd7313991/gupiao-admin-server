package mobile

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"github.com/gin-gonic/gin"
)

func TestTransferAmountLimits(t *testing.T) {
	for _, tc := range []struct {
		name    string
		maximum float64
		amount  string
		pin     string
		allowed bool
		hint    string
	}{
		{"提现超过原500万上限", maxWithdrawalAmount, "5000000.01", "123456", true, ""},
		{"提现恰好5000万", maxWithdrawalAmount, "50000000", "123456", true, ""},
		{"提现超过5000万一分钱", maxWithdrawalAmount, "50000000.01", "123456", false, "5000 万元"},
		{"提现低于最低金额", maxWithdrawalAmount, "99.99", "123456", false, "5000 万元"},
		{"提现金额超过两位小数", maxWithdrawalAmount, "100.001", "123456", false, "5000 万元"},
		{"提现交易密码无效", maxWithdrawalAmount, "50000000", "12345", false, "6 位数字"},
		{"充值仍允许500万", maxTransferAmount, "5000000", "123456", true, ""},
		{"充值仍拒绝超过500万", maxTransferAmount, "5000000.01", "123456", false, "500 万元"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			router := gin.New()
			router.POST("/transfer", func(c *gin.Context) {
				if _, ok := bindTransferInput(c, tc.maximum); ok {
					c.JSON(http.StatusOK, gin.H{"code": 200})
				}
			})
			request := httptest.NewRequest(http.MethodPost, "/transfer", strings.NewReader(`{"amount":`+tc.amount+`,"trade_pin":"`+tc.pin+`"}`))
			request.Header.Set("Content-Type", "application/json")
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, request)
			var result struct {
				Code int `json:"code"`
			}
			if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
				t.Fatal(err)
			}
			if (result.Code == 200) != tc.allowed || (tc.hint != "" && !strings.Contains(recorder.Body.String(), tc.hint)) {
				t.Fatalf("金额校验结果错误：%s", recorder.Body.String())
			}
		})
	}
}

func TestWithdrawalTimeBeijingBoundaries(t *testing.T) {
	settings, err := parseMobileTradeSettings(`{"trade":{"allDay":true},"recharge":{"withdrawStart":"09:00:00","withdrawEnd":"16:00:00"}}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		at      string
		allowed bool
	}{
		{"2026-11-17T08:59:59+08:00", false},
		{"2026-11-17T09:00:00+08:00", true},
		{"2026-11-17T15:59:59.999+08:00", true},
		{"2026-11-17T16:00:00+08:00", false},
		{"2026-11-17T21:02:14+08:00", false},
		{"2026-11-17T01:00:00Z", true},
		{"2026-11-17T08:00:00Z", false},
		{"2026-11-17T01:00:00-05:00", true},
		{"2026-11-21T10:00:00+08:00", true},
	} {
		t.Run(tc.at, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339Nano, tc.at)
			if err != nil {
				t.Fatal(err)
			}
			err = validateWithdrawalTime(now, settings.Recharge.WithdrawStart, settings.Recharge.WithdrawEnd)
			if (err == nil) != tc.allowed {
				t.Fatalf("提现时段判断错误：allowed=%v，err=%v", tc.allowed, err)
			}
			if err != nil && !strings.Contains(err.Error(), "北京时间 09:00:00 至 16:00:00") {
				t.Fatalf("拒绝提示未说明有效时段：%v", err)
			}
		})
	}
}

func TestWithdrawalTimeInvalidConfigFailsClosed(t *testing.T) {
	now := time.Date(2026, 11, 17, 10, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	for _, config := range []string{
		`{}`,
		`{"recharge":{"withdrawStart":"","withdrawEnd":"16:00:00"}}`,
		`{"recharge":{"withdrawStart":"09:00","withdrawEnd":"16:00:00"}}`,
		`{"recharge":{"withdrawStart":"09:00:00","withdrawEnd":"24:00:00"}}`,
		`{"recharge":{"withdrawStart":"09:00:00","withdrawEnd":"09:00:00"}}`,
		`{"recharge":{"withdrawStart":"22:00:00","withdrawEnd":"06:00:00"}}`,
		`{"recharge":{"withdrawStart":"09:00:00.1","withdrawEnd":"16:00:00"}}`,
	} {
		settings, err := parseMobileTradeSettings(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := validateWithdrawalTime(now, settings.Recharge.WithdrawStart, settings.Recharge.WithdrawEnd); err == nil || !strings.Contains(err.Error(), "配置异常") {
			t.Fatalf("无效配置不应放行：%s，%v", config, err)
		}
	}
}

func TestWithdrawalTimeReadsLatestSettingsIntegration(t *testing.T) {
	db := financingTestDB(t)
	setting := system.AppSystemSetting{Config: `{"recharge":{"withdrawStart":"09:00:00","withdrawEnd":"16:00:00"}}`}
	if err := db.Create(&setting).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 17, 17, 0, 0, 0, time.FixedZone("UTC+8", 8*3600))
	if err := checkWithdrawalTime(db, now); err == nil {
		t.Fatal("17点不应允许提交提现")
	}
	if err := db.Model(&setting).Update("config", `{"recharge":{"withdrawStart":"09:00:00","withdrawEnd":"18:00:00"}}`).Error; err != nil {
		t.Fatal(err)
	}
	if err := checkWithdrawalTime(db, now); err != nil {
		t.Fatalf("应立即使用修改后的时段：%v", err)
	}
	if err := db.Model(&setting).Update("config", `{}`).Error; err != nil {
		t.Fatal(err)
	}
	if err := checkWithdrawalTime(db, now); err == nil {
		t.Fatal("配置缺失时不能放行")
	}
	if err := db.Delete(&setting).Error; err != nil {
		t.Fatal(err)
	}
	if err := checkWithdrawalTime(db, now); err == nil {
		t.Fatal("读取配置失败时不能放行")
	}
}
