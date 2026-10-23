package stock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"api-server/db/pgdb/system"
)

func hongKongQuoteFixture(code, price, timestamp string) string {
	fields := make([]string, 38)
	fields[0], fields[1], fields[2] = "100", "测试港股", code
	fields[3], fields[6], fields[30], fields[32], fields[37] = price, "1234", timestamp, "-1.77", "506400.5"
	return "v_hk" + code + `="` + strings.Join(fields, "~") + `";`
}

func TestSinaHongKongToSecurity(t *testing.T) {
	item := sinaHongKongSecurity{Code: "00700", Name: "腾讯控股", Price: "410.123", ChangeRate: "-1.77", Volume: "1234", Amount: "506400.5", QuoteTime: "2026/10/20 14:00:00"}
	security, err := sinaHongKongToSecurity(item)
	if err != nil {
		t.Fatal(err)
	}
	wantTime := time.Date(2026, 10, 20, 6, 0, 0, 0, time.UTC).Unix()
	if security.Symbol != "00700.HK" || security.Code != "00700" || security.Market != "港股" || security.Exchange != "港交所" || security.Status != system.StatusEnabled {
		t.Fatalf("港股身份或默认状态错误：%+v", security)
	}
	if security.LastPrice != 410.123 || security.ChangeRate != -1.77 || security.Volume != 1234 || security.Amount != 506400.5 || security.QuoteAt != wantTime || security.Source != "sina" {
		t.Fatalf("港股行情或时间解析错误：%+v", security)
	}
	item.Price, item.QuoteTime = "0", ""
	unavailable, err := sinaHongKongToSecurity(item)
	if err != nil || unavailable.Source != "sina/unavailable" || unavailable.QuoteAt != 0 {
		t.Fatalf("无报价标识错误：%+v，%v", unavailable, err)
	}
	for _, price := range []string{"NaN", "+Inf", "错误", "-1"} {
		item.Price = price
		if _, err := sinaHongKongToSecurity(item); err == nil {
			t.Fatalf("未拒绝无效价格：%s", price)
		}
	}
	item.Price, item.QuoteTime = "410", "invalid"
	if _, err := sinaHongKongToSecurity(item); err == nil {
		t.Fatal("未拒绝无效报价时间")
	}
}

func TestParseHongKongTencentQuotes(t *testing.T) {
	body := "\n " + hongKongQuoteFixture("00700", "410.123", "2026/10/20 14:00:00") +
		"\r\n " + hongKongQuoteFixture("09988", "104.7", "2026/10/20 14:00:01") +
		"\n " + hongKongQuoteFixture("00005", "0", "")
	quotes, err := parseHongKongTencentQuotes(body)
	if err != nil {
		t.Fatal(err)
	}
	if len(quotes) != 2 || quotes["00700"].LastPrice != 410.123 || quotes["09988"].QuoteAt != quotes["00700"].QuoteAt+1 {
		t.Fatalf("腾讯港股多行或精度解析错误：%+v", quotes)
	}
	for _, invalid := range []string{
		"", "<html>502 Bad Gateway</html>", `v_hk00700="incomplete";`,
		strings.Replace(hongKongQuoteFixture("00700", "1", "2026/10/20 14:00:00"), "~00700~", "~09988~", 1),
		hongKongQuoteFixture("00700", "1", "invalid"),
	} {
		if _, err := parseHongKongTencentQuotes(invalid); err == nil {
			t.Fatalf("未拒绝无效腾讯响应：%s", invalid)
		}
	}
}

func TestCollectHongKongUntilEmptyPage(t *testing.T) {
	listCalls, quoteCalls := 0, 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/list":
			listCalls++
			if r.URL.Query().Get("num") != "60" || r.URL.Query().Get("node") != "qbgg_hk" || r.URL.Query().Get("sort") != "symbol" {
				t.Error("新浪分页参数错误")
			}
			var items []sinaHongKongSecurity
			switch r.URL.Query().Get("page") {
			case "1":
				items = []sinaHongKongSecurity{
					{Code: "00005", Name: "汇丰控股", Price: "145", QuoteTime: "2026/10/20 14:00:00"},
					{Code: "00700", Name: "腾讯控股", Price: "410", QuoteTime: "2026/10/20 14:00:00"},
				}
			case "2":
				items = []sinaHongKongSecurity{{Code: "09988", Name: "阿里巴巴", Price: "0"}}
			default:
				items = []sinaHongKongSecurity{}
			}
			if err := json.NewEncoder(w).Encode(items); err != nil {
				t.Error(err)
			}
		case "/quotes":
			quoteCalls++
			if r.URL.Query().Get("q") != "r_hk00005,hk00005,r_hk00700,hk00700,r_hk09988,hk09988" {
				t.Error("腾讯港股代码未保留前导零")
			}
			fmt.Fprint(w, hongKongQuoteFixture("00005", "145.5", "2026/10/20 14:00:01")+"\n"+
				hongKongQuoteFixture("00700", "409", "2026/10/20 13:59:59"))
		case "/eastmoney":
			fmt.Fprint(w, `{"rc":0,"data":{"diff":[]}}`)
		default:
			t.Errorf("不应请求总数接口：%s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	var progress []hongKongSyncStatus
	rows, result, err := collectHongKong(context.Background(), server.Client(), server.URL+"/list", server.URL+"/quotes", server.URL+"/eastmoney", func(status hongKongSyncStatus) {
		progress = append(progress, status)
	})
	if err != nil {
		t.Fatal(err)
	}
	if listCalls != 3 || quoteCalls != 1 || len(rows) != 3 || result.Pages != 2 || result.Fetched != 3 || result.Quoted != 3 {
		t.Fatalf("采集分页错误：%+v，list=%d，quote=%d", result, listCalls, quoteCalls)
	}
	if result.Tencent != 1 || result.Sina != 1 || result.Unavailable != 1 || result.Synced != 0 {
		t.Fatalf("报价来源计数或未入库状态错误：%+v", result)
	}
	if rows[0].LastPrice != 145.5 || rows[0].Source != "sina/tencent" || rows[1].LastPrice != 410 || rows[1].Source != "sina" || rows[2].Source != "sina/unavailable" {
		t.Fatalf("未选择较新的有效报价：%+v", rows)
	}
	if len(progress) == 0 || progress[len(progress)-1].Stage != "正在保存港股数据" {
		t.Fatalf("未报告完整采集进度：%+v", progress)
	}
}

func TestCollectHongKongRejectsInvalidResponses(t *testing.T) {
	for _, test := range []struct {
		name string
		body string
	}{
		{"空列表", "[]"},
		{"空对象", "{}"},
		{"空响应", "null"},
		{"上游业务错误", `{"__ERROR":3,"__ERRORMSG":"Service not valid"}`},
		{"无效代码", `[{"symbol":"700","name":"腾讯"}]`},
		{"重复代码", `[{"symbol":"00700","name":"腾讯"},{"symbol":"00700","name":"腾讯"}]`},
		{"乱序代码", `[{"symbol":"09988","name":"阿里"},{"symbol":"00700","name":"腾讯"}]`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, test.body)
			}))
			defer server.Close()
			rows, _, err := collectHongKong(context.Background(), server.Client(), server.URL, server.URL, server.URL, func(hongKongSyncStatus) {})
			if err == nil || len(rows) != 0 {
				t.Fatalf("无效采集返回成功或部分数据：rows=%d，err=%v", len(rows), err)
			}
		})
	}
}

func TestCollectHongKongQuoteSources(t *testing.T) {
	at := time.Date(2026, 10, 22, 16, 8, 39, 0, hkLocation)
	for _, tc := range []struct {
		name       string
		primaryAge time.Duration
		primaryBad bool
		tencentBad bool
		wantSource string
		wantPrice  float64
	}{
		{"东方财富较新", -time.Minute, false, false, "eastmoney", 3.155},
		{"相同时间优先东方财富", 0, false, false, "eastmoney", 3.155},
		{"腾讯较新", time.Minute, false, false, "sina/tencent", 3.15},
		{"东方财富失败使用腾讯", 0, true, false, "sina/tencent", 3.15},
		{"腾讯失败使用东方财富", 0, false, true, "eastmoney", 3.155},
		{"两个报价源失败保留新浪", 0, true, true, "sina", 3.1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/list":
					if r.URL.Query().Get("page") == "1" {
						fmt.Fprintf(w, `[{"symbol":"09889","name":"测试港股","lasttrade":"3.1","ticktime":"%s"}]`, at.Add(-2*time.Minute).Format("2006/01/02 15:04:05"))
					} else {
						fmt.Fprint(w, `[]`)
					}
				case "/tencent":
					if tc.tencentBad {
						w.WriteHeader(http.StatusBadGateway)
					} else {
						fmt.Fprint(w, hongKongQuoteFixture("09889", "3.15", at.Format("2006/01/02 15:04:05")))
					}
				case "/eastmoney":
					if tc.primaryBad {
						w.WriteHeader(http.StatusBadGateway)
					} else {
						fmt.Fprintf(w, `{"rc":0,"data":{"diff":[{"f12":"09889","f13":116,"f2":3.155,"f3":6.06,"f5":330000,"f6":1003630,"f124":%d}]}}`, at.Add(-tc.primaryAge).Unix())
					}
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			rows, result, err := collectHongKong(context.Background(), server.Client(), server.URL+"/list", server.URL+"/tencent", server.URL+"/eastmoney", func(hongKongSyncStatus) {})
			if err != nil || len(rows) != 1 || rows[0].Source != tc.wantSource || rows[0].LastPrice != tc.wantPrice ||
				result.Eastmoney+result.Tencent+result.Sina != 1 {
				t.Fatalf("来源选择错误：%+v %+v %v", rows, result, err)
			}
		})
	}
}

func TestFetchHongKongBodyFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	if _, err := fetchHongKongBody(context.Background(), server.Client(), server.URL, "", false); err == nil {
		t.Fatal("未拒绝上游 HTTP 错误")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := fetchHongKongBody(ctx, server.Client(), server.URL, "", false); err == nil {
		t.Fatal("取消请求未停止")
	}
	if err := waitHongKongRequest(ctx); err == nil {
		t.Fatal("取消后仍继续下一批请求")
	}
}

func TestSecurityResponseIncludesQuoteTime(t *testing.T) {
	item := system.StockSecurity{Code: "00700", QuoteAt: 1792476000, Source: "sina/tencent"}
	response := toResponse(item)
	if response.QuoteAt != item.QuoteAt || response.Source != item.Source {
		t.Fatalf("后台响应遗漏报价时间或来源：%+v", response)
	}
}
