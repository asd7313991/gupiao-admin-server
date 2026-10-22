package mobile

import (
	"math"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api-server/config"
	"github.com/gin-gonic/gin"
)

func TestParseHongKongTradeQuote(t *testing.T) {
	fields := make([]string, 78)
	for i := range fields {
		fields[i] = "0"
	}
	fields[2], fields[3], fields[30], fields[32], fields[60], fields[63], fields[75] = "03774", "35.500", "2026/10/22 10:49:41", "1.5", "1000", "GP", "HKD"
	raw := func() string { return `v_hk03774="` + strings.Join(fields, "~") + `";` }
	quote, err := parseHongKongTradeQuote(raw(), "03774")
	if err != nil || quote.LotSize != 1000 || quote.Currency != "HKD" || quote.LastPrice != 35.5 {
		t.Fatalf("quote=%+v err=%v", quote, err)
	}
	for _, index := range []int{3, 30, 32, 60} {
		original := fields[index]
		fields[index] = "invalid"
		if _, err := parseHongKongTradeQuote(raw(), "03774"); err == nil {
			t.Fatalf("应拒绝无效字段%d", index)
		}
		fields[index] = original
	}
	if _, err := parseHongKongTradeQuote(raw(), "00700"); err == nil {
		t.Fatal("代码不匹配应拒绝")
	}
	if _, err := parseHongKongTradeQuote(`v_hk03774="100~short";`, "03774"); err == nil {
		t.Fatal("缺字段应拒绝")
	}
	now := time.Unix(quote.QuoteAt+15*60, 0)
	if message := validateHongKongQuote(quote, now); message != "" {
		t.Fatal(message)
	}
	for _, tc := range []struct {
		name   string
		change func(*hongKongQuote)
	}{
		{"人民币柜台", func(q *hongKongQuote) { q.Currency = "CNY" }},
		{"基金", func(q *hongKongQuote) { q.Kind = "ETF" }},
		{"报价过期", func(q *hongKongQuote) { q.QuoteAt = now.Add(-31 * time.Minute).Unix() }},
		{"未来时间", func(q *hongKongQuote) { q.QuoteAt = now.Add(2 * time.Minute).Unix() }},
		{"零价格", func(q *hongKongQuote) { q.LastPrice = 0 }},
		{"非数价格", func(q *hongKongQuote) { q.LastPrice = math.NaN() }},
		{"零每手数", func(q *hongKongQuote) { q.LotSize = 0 }},
		{"小数每手数", func(q *hongKongQuote) { q.LotSize = 1.5 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := quote
			tc.change(&q)
			if validateHongKongQuote(q, now) == "" {
				t.Fatal("无效报价不应被允许")
			}
		})
	}
}

func TestHongKongTradingTime(t *testing.T) {
	var settings mobileTradeSettings
	for _, tc := range []struct {
		clock string
		want  bool
	}{
		{"2026-10-22 09:29:59", false}, {"2026-10-22 09:30:00", true}, {"2026-10-22 11:59:00", true},
		{"2026-10-22 12:15:00", false}, {"2026-10-22 13:00:00", true}, {"2026-10-22 15:59:59", true},
		{"2026-10-22 16:01:00", false}, {"2026-10-24 10:00:00", false},
	} {
		now, _ := time.ParseInLocation("2006-01-02 15:04:05", tc.clock, time.FixedZone("HK", 8*3600))
		if got := hongKongTradeTime(now, settings); got != tc.want {
			t.Errorf("%s got=%v", tc.clock, got)
		}
	}
}

func TestHongKongClosePriceTest(t *testing.T) {
	location := time.FixedZone("HK", 8*3600)
	at := func(value string) time.Time {
		result, err := time.ParseInLocation("2006-01-02 15:04:05", value, location)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	now := at("2026-10-22 17:10:00")
	quote := hongKongQuote{LastPrice: 0.95, LotSize: 4000, Currency: "HKD", Kind: "GP", QuoteAt: at("2026-10-22 16:08:42").Unix()}
	var settings mobileTradeSettings
	settings.HKTrade.LocalCloseTest = true
	for _, tc := range []struct {
		name    string
		now     time.Time
		quoteAt int64
		local   bool
		enabled bool
		want    bool
	}{
		{"当日收市报价", now, quote.QuoteAt, true, true, true},
		{"收市前", at("2026-10-22 16:09:59"), quote.QuoteAt, true, true, false},
		{"收市边界", at("2026-10-22 16:10:00"), quote.QuoteAt, true, true, true},
		{"仅上午报价", now, at("2026-10-22 11:52:00").Unix(), true, true, false},
		{"昨天报价", now, at("2026-10-21 16:08:42").Unix(), true, true, false},
		{"跨日", at("2026-10-23 00:00:00"), quote.QuoteAt, true, true, false},
		{"未来报价", now, now.Add(2 * time.Minute).Unix(), true, true, false},
		{"非本机", now, quote.QuoteAt, false, true, false},
		{"开关关闭", now, quote.QuoteAt, true, false, false},
		{"周末", at("2026-10-24 17:10:00"), at("2026-10-24 16:08:42").Unix(), true, true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q, s := quote, settings
			q.QuoteAt = tc.quoteAt
			s.HKTrade.LocalCloseTest = tc.enabled
			if got := hongKongClosePriceTest(q, tc.now, s, tc.local); got != tc.want {
				t.Fatalf("收市测试模式=%v，期望%v", got, tc.want)
			}
		})
	}
	if validateHongKongQuote(quote, now) == "" {
		t.Fatal("普通模式仍须拒绝过期价")
	}
	if reason := validateHongKongQuoteWithCloseTest(quote, now, hongKongClosePriceTest(quote, now, settings, true)); reason != "" {
		t.Fatal(reason)
	}
	for _, change := range []func(*hongKongQuote){
		func(q *hongKongQuote) { q.LastPrice = 0 },
		func(q *hongKongQuote) { q.Currency = "CNY" },
		func(q *hongKongQuote) { q.Kind = "ETF" },
		func(q *hongKongQuote) { q.LotSize = 1.5 },
	} {
		q := quote
		change(&q)
		if validateHongKongQuoteWithCloseTest(q, now, hongKongClosePriceTest(q, now, settings, true)) == "" {
			t.Fatal("收市测试不应跳过价格、币种、品种及整手校验")
		}
	}
}

func TestLocalHongKongTestRequest(t *testing.T) {
	original := config.RunModel
	t.Cleanup(func() { config.RunModel = original })
	for _, tc := range []struct {
		mode   string
		remote string
		want   bool
	}{
		{config.RunModelDevValue, "127.0.0.1:3456", true},
		{config.RunModelDevValue, "[::1]:3456", true},
		{config.RunModelDevValue, "192.168.1.10:3456", false},
		{config.RunModelRelease, "127.0.0.1:3456", false},
		{config.RunModelDevValue, "invalid", false},
	} {
		config.RunModel = tc.mode
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Request = httptest.NewRequest("GET", "http://localhost/trade", nil)
		c.Request.RemoteAddr = tc.remote
		if got := localHongKongTestRequest(c); got != tc.want {
			t.Fatalf("模式=%s 地址=%s，允许=%v", tc.mode, tc.remote, got)
		}
		if tc.want {
			c.Request.Header.Set("X-Forwarded-For", "203.0.113.10")
			if localHongKongTestRequest(c) {
				t.Fatal("经本机代理转发的远程客户端不得使用收市价测试")
			}
		}
	}
}
