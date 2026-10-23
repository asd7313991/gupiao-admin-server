package mobile

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"api-server/config"
	"github.com/gin-gonic/gin"
)

func TestHongKongTradeQuoteSources(t *testing.T) {
	at := time.Date(2026, 10, 22, 16, 8, 39, 0, time.FixedZone("HK", 8*3600))
	for _, tc := range []struct {
		name        string
		primaryAge  time.Duration
		primaryBad  bool
		primaryZero bool
		wantSource  string
		wantPrice   float64
	}{
		{"较新东方财富报价", -time.Minute, false, false, "eastmoney", 3.155},
		{"同时间优先东方财富", 0, false, false, "eastmoney", 3.155},
		{"不采用更旧东方财富", time.Minute, false, false, "tencent", 3.15},
		{"东方财富失败使用腾讯", 0, true, false, "tencent", 3.15},
		{"东方财富零价使用腾讯", 0, false, true, "tencent", 3.15},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/eastmoney" {
					if tc.primaryBad {
						w.WriteHeader(http.StatusBadGateway)
						return
					}
					price := 3.155
					if tc.primaryZero {
						price = 0
					}
					fmt.Fprintf(w, `{"rc":0,"data":{"diff":[{"f12":"09889","f13":116,"f2":%g,"f3":6.06,"f5":330000,"f6":1003630,"f124":%d}]}}`, price, at.Add(-tc.primaryAge).Unix())
					return
				}
				fields := make([]string, 78)
				fields[2], fields[3], fields[30], fields[32], fields[60], fields[63], fields[75] = "09889", "3.150", at.Format("2006/01/02 15:04:05"), "6.06", "1000", "GP", "HKD"
				fields[9], fields[10], fields[19], fields[20] = "3.14", "1000", "3.16", "1000"
				fmt.Fprint(w, `v_hk09889="`+strings.Join(fields, "~")+`";`)
			}))
			defer server.Close()
			quote, err := fetchHongKongTradeQuoteWithSources(context.Background(), server.Client(), "09889", server.URL+"/tencent/", server.URL+"/eastmoney")
			if err != nil || quote.Source != tc.wantSource || quote.LastPrice != tc.wantPrice || quote.DepthAt != at.Unix() ||
				quote.LotSize != 1000 || quote.Currency != "HKD" || quote.Ask[0].Price != 3.16 {
				t.Fatalf("报价选择或独立盘口时间错误：%+v %v", quote, err)
			}
			expectedAt := at.Unix()
			if tc.wantSource == "eastmoney" {
				expectedAt = at.Add(-tc.primaryAge).Unix()
			}
			if quote.QuoteAt != expectedAt {
				t.Fatal("不得将请求时间写成报价时间")
			}
		})
	}
}

func TestHongKongClosePriceSimulation(t *testing.T) {
	location := time.FixedZone("HK", 8*3600)
	at := func(value string) time.Time {
		result, err := time.ParseInLocation("2006-01-02 15:04:05", value, location)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	var settings mobileTradeSettings
	settings.Trade.AllDay = true
	for _, tc := range []struct {
		name, now, quote string
		want             bool
	}{
		{"次日开市前", "2026-10-23 08:30:00", "2026-10-22 16:08:39", true},
		{"开市前边界", "2026-10-23 09:29:59", "2026-10-22 16:08:39", true},
		{"开市恢复时效", "2026-10-23 09:30:00", "2026-10-22 16:08:39", false},
		{"交易时段拒绝旧价", "2026-10-23 11:00:00", "2026-10-22 16:08:39", false},
		{"午休不使用昨日收市价", "2026-10-23 12:30:00", "2026-10-22 16:08:39", false},
		{"收市当晚", "2026-10-23 18:00:00", "2026-10-23 16:08:39", true},
		{"收市后不能用前日价", "2026-10-23 18:00:00", "2026-10-22 16:08:39", false},
		{"周六使用周五", "2026-10-24 10:00:00", "2026-10-23 16:08:39", true},
		{"周日使用周五", "2026-10-25 20:00:00", "2026-10-23 16:08:39", true},
		{"周一开市前使用周五", "2026-10-26 08:30:00", "2026-10-23 16:08:39", true},
		{"周一开市后拒绝周五", "2026-10-26 09:30:00", "2026-10-23 16:08:39", false},
		{"周末拒绝周四", "2026-10-24 10:00:00", "2026-10-22 16:08:39", false},
		{"拒绝非收市阶段", "2026-10-23 08:30:00", "2026-10-22 15:59:59", false},
		{"收市阶段下界", "2026-10-23 08:30:00", "2026-10-22 16:00:00", true},
		{"收市阶段上界", "2026-10-23 08:30:00", "2026-10-22 16:30:00", false},
		{"拒绝未来时间", "2026-10-23 08:30:00", "2026-10-23 16:08:39", false},
		{"新报价不标记收市回放", "2026-10-23 16:38:39", "2026-10-23 16:08:39", false},
		{"超过30分钟才标记回放", "2026-10-23 16:38:40", "2026-10-23 16:08:39", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			now := at(tc.now)
			quote := hongKongQuote{Code: "09889", LastPrice: 3.15, LotSize: 1000, Kind: "GP", Currency: "HKD", QuoteAt: at(tc.quote).Unix()}
			if got := hongKongClosePriceSimulation(quote, now, settings); got != tc.want {
				t.Fatalf("收市模拟=%v，期望%v", got, tc.want)
			}
			disabled := settings
			disabled.Trade.AllDay = false
			if hongKongClosePriceSimulation(quote, now, disabled) {
				t.Fatal("关闭全天模式后不得放行")
			}
			if tc.want {
				if reason := validateHongKongQuoteWithCloseTest(quote, now, true); reason != "" {
					t.Fatal(reason)
				}
				for _, change := range []func(*hongKongQuote){
					func(q *hongKongQuote) { q.LastPrice = 0 },
					func(q *hongKongQuote) { q.LotSize = 1.5 },
					func(q *hongKongQuote) { q.Currency = "CNY" },
					func(q *hongKongQuote) { q.Kind = "ETF" },
				} {
					invalid := quote
					change(&invalid)
					if validateHongKongQuoteWithCloseTest(invalid, now, hongKongClosePriceSimulation(invalid, now, settings)) == "" {
						t.Fatal("收市价模拟不得跳过交易元数据校验")
					}
				}
			}
		})
	}
}

func TestHongKongTradeRequiresMetadata(t *testing.T) {
	at := time.Now().Add(-15 * time.Minute)
	for _, tc := range []struct {
		name, price, lot, kind, currency string
		wantError                        bool
	}{
		{"腾讯零价仍可用东方财富价格", "0", "1000", "GP", "HKD", false},
		{"每手缺失不能猜测", "3.15", "0", "GP", "HKD", true},
		{"元数据字段无效", "3.15", "invalid", "GP", "HKD", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/eastmoney" {
					fmt.Fprintf(w, `{"rc":0,"data":{"diff":[{"f12":"09889","f13":116,"f2":3.155,"f3":6.06,"f5":330000,"f6":1003630,"f124":%d}]}}`, at.Unix())
					return
				}
				fields := make([]string, 78)
				fields[2], fields[3], fields[30], fields[32], fields[60], fields[63], fields[75] = "09889", tc.price, at.Format("2006/01/02 15:04:05"), "6.06", tc.lot, tc.kind, tc.currency
				fmt.Fprint(w, `v_hk09889="`+strings.Join(fields, "~")+`";`)
			}))
			defer server.Close()
			quote, err := fetchHongKongTradeQuoteWithSources(context.Background(), server.Client(), "09889", server.URL+"/tencent/", server.URL+"/eastmoney")
			if (err != nil) != tc.wantError {
				t.Fatalf("quote=%+v err=%v", quote, err)
			}
			if !tc.wantError && (quote.Source != "eastmoney" || quote.LastPrice != 3.155 || quote.LotSize != 1000) {
				t.Fatalf("应保留腾讯元数据并采用东方财富有效价格：%+v", quote)
			}
		})
	}
}

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

func TestHongKongTradeQuoteLive(t *testing.T) {
	if os.Getenv("HK_QUOTE_LIVE_TEST") != "1" {
		t.Skip("未设置 HK_QUOTE_LIVE_TEST=1，跳过公开行情联网验证")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	quote, err := fetchHongKongTradeQuote(ctx, "09889")
	if err != nil {
		t.Fatal(err)
	}
	if quote.LastPrice <= 0 || quote.QuoteAt <= 0 || quote.DepthAt <= 0 || quote.QuoteAt > time.Now().Unix()+60 ||
		quote.LotSize <= 0 || quote.Currency != "HKD" || quote.Kind != "GP" ||
		(quote.Source != "eastmoney" && quote.Source != "tencent") {
		t.Fatalf("公开港股报价或交易元数据无效：%+v", quote)
	}
	t.Logf("来源=%s，价格=%.3f，每手=%.0f，原始报价时间=%d，独立盘口时间=%d", quote.Source, quote.LastPrice, quote.LotSize, quote.QuoteAt, quote.DepthAt)
}
