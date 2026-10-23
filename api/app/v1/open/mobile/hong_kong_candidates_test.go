package mobile

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"api-server/util/hkquote"
)

func TestHongKongProblemQuoteLive(t *testing.T) {
	if os.Getenv("HK_QUOTE_LIVE_TEST") != "1" {
		t.Skip("设置 HK_QUOTE_LIVE_TEST=1 后只读核对00875港股候选，不下单")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	quote, err := fetchHongKongTradeQuote(ctx, "00875")
	if err != nil {
		t.Fatal(err)
	}
	if quote.Code != "00875" || quote.LastPrice <= 0 || quote.LotSize <= 0 || quote.MetadataAt <= 0 {
		t.Fatalf("问题股票实际报价或证券资料解析失败：%+v", quote)
	}
	t.Logf("00875.HK价格=%.3f，源=%s，原始时间=%s，每手=%.0f，盘口时间=%d，当前交易校验=%q",
		quote.LastPrice, quote.Source, time.Unix(quote.QuoteAt, 0).In(time.FixedZone("HK", 8*3600)).Format("2006-01-02 15:04:05"),
		quote.LotSize, quote.DepthAt, validateHongKongQuote(quote, time.Now()))
}

func hkTradingCandidate(alias, code, price string, at time.Time) string {
	fields := make([]string, 78)
	fields[2], fields[3], fields[30], fields[32], fields[60], fields[63], fields[75] =
		code, price, at.In(time.FixedZone("HK", 8*3600)).Format("2006/01/02 15:04:05"), "1", "2000", "GP", "HKD"
	fields[9], fields[10], fields[19], fields[20] = price, "0", price, "0"
	return "v_" + alias + code + `="` + strings.Join(fields, "~") + `";`
}

func TestHongKongTradingCandidatesUseNewerOriginalPriceWithoutFakeDepth(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	for _, tc := range []struct {
		name, candidate, wantPrice string
	}{
		{"r_hk较新", hkTradingCandidate("r_hk", "00875", "0.34", now.Add(-time.Minute)), "0.34"},
		{"r_hk缺失", "", "0.335"},
		{"r_hk畸形", `v_r_hk00875="bad";`, "0.335"},
		{"r_hk未来时间", hkTradingCandidate("r_hk", "00875", "9", now.Add(61*time.Second)), "0.335"},
		{"r_hk重复", hkTradingCandidate("r_hk", "00875", "0.34", now.Add(-time.Minute)) + hkTradingCandidate("r_hk", "00875", "0.34", now.Add(-time.Minute)), "0.335"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("q") != "r_hk00875,hk00875" {
					t.Error("交易取价必须请求两个腾讯候选且保留代码前导零")
				}
				fmt.Fprint(w, tc.candidate+hkTradingCandidate("hk", "00875", "0.335", now.Add(-10*time.Minute)))
			}))
			defer server.Close()
			quote, err := fetchTencentHongKongTradeQuote(context.Background(), server.Client(), "00875", server.URL+"?q=hk")
			if err != nil || fmt.Sprint(quote.LastPrice) != tc.wantPrice || quote.DepthAt != 0 || quote.MetadataAt != quote.QuoteAt ||
				quote.Bid[0].Volume != 0 || quote.Ask[0].Volume != 0 || quote.QuoteAt == now.Unix() {
				t.Fatalf("候选择新、回退或空盘口时间错误：%+v %v", quote, err)
			}
		})
	}
}

func TestHongKongMetadataFallbackNeverRestoresOldPricesOrDepth(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fresh := hkquote.Metadata{LotSize: 2000, Currency: "HKD", Kind: "GP", At: now.Add(-time.Hour).Unix()}
	for _, tc := range []struct {
		name      string
		metadata  hkquote.Metadata
		quoteAge  time.Duration
		brokenEM  bool
		wantError bool
	}{
		{"资料有效且东方财富报价新", fresh, time.Minute, false, false},
		{"资料缺失", hkquote.Metadata{}, time.Minute, false, true},
		{"资料过期", hkquote.Metadata{LotSize: 2000, Currency: "HKD", Kind: "GP", At: now.Add(-24*time.Hour - time.Second).Unix()}, time.Minute, false, true},
		{"资料未来", hkquote.Metadata{LotSize: 2000, Currency: "HKD", Kind: "GP", At: now.Add(61 * time.Second).Unix()}, time.Minute, false, true},
		{"无行情不能拿资料充当价格", fresh, time.Minute, true, true},
		{"旧价格仍由交易时效拒绝", fresh, 31 * time.Minute, false, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/eastmoney" || tc.brokenEM {
					http.Error(w, "unavailable", http.StatusBadGateway)
					return
				}
				fmt.Fprintf(w, `{"rc":0,"data":{"diff":[{"f12":"00875","f13":116,"f2":0.34,"f3":1,"f5":1,"f6":1,"f124":%d}]}}`, now.Add(-tc.quoteAge).Unix())
			}))
			defer server.Close()
			quote, err := fetchHongKongTradeQuoteWithSources(context.Background(), server.Client(), "00875", server.URL+"/tencent?q=hk", server.URL+"/eastmoney", tc.metadata)
			if tc.wantError {
				if err == nil {
					t.Fatal("不能使用缺失、过期或未来资料，亦不能使用不存在的动态行情")
				}
				return
			}
			if err != nil || quote.LastPrice != 0.34 || quote.Source != "eastmoney" || quote.MetadataAt != fresh.At ||
				quote.QuoteAt != now.Add(-tc.quoteAge).Unix() || quote.DepthAt != 0 || len(quote.Bid) != 0 || quote.SyncWarning == "" {
				t.Fatalf("资料容错只能恢复资料，不能恢复缓存价格或盘口：%+v %v", quote, err)
			}
			if stale := validateHongKongQuote(quote, now) != ""; stale != (tc.quoteAge > 30*time.Minute) {
				t.Fatalf("交易价格时效不能随资料回退放宽：%+v", quote)
			}
		})
	}
}
