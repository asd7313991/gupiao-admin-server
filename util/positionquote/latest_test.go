package positionquote

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"api-server/util/hkquote"
)

func TestLatestHongKongPositionsSelectPerSecurity(t *testing.T) {
	now := clock("2026-10-23 11:20:00")
	for _, tc := range []struct {
		name, tencentTime, eastmoneyTime, source string
		price                                    float64
		wantError                                bool
	}{
		{"东方财富较新", "2026/10/23 11:18:00", "2026-10-23 11:19:00", "eastmoney", 3.2, false},
		{"腾讯较新", "2026/10/23 11:19:00", "2026-10-23 11:18:00", "tencent", 3.1, false},
		{"同时间优先东方财富", "2026/10/23 11:19:00", "2026-10-23 11:19:00", "eastmoney", 3.2, false},
		{"腾讯过期用东方财富", "2026/10/21 16:00:00", "2026-10-23 11:19:00", "eastmoney", 3.2, false},
		{"东方财富过期用腾讯", "2026/10/23 11:19:00", "2026-10-21 16:00:00", "tencent", 3.1, false},
		{"主备都过期", "2026/10/21 16:00:00", "2026-10-21 16:00:00", "", 0, true},
		{"未来时间不能覆盖腾讯", "2026/10/23 11:19:00", "2026-10-23 11:21:01", "tencent", 3.1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if request.URL.Path == "/tencent" {
					fmt.Fprint(w, fixture("hk09889", "09889", "3.1", tc.tencentTime))
					return
				}
				json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"diff": []map[string]any{
					{"f12": "09889", "f13": 116, "f2": 3.2, "f3": 1, "f5": 100, "f6": 320, "f124": clock(tc.eastmoneyTime).Unix()},
				}}})
			}))
			defer server.Close()
			quotes, err := FetchLatest(context.Background(), server.Client(), server.URL+"/tencent?q=", server.URL+"/eastmoney", []string{"09889.HK", "09889.HK"}, now)
			if tc.wantError {
				if err == nil || quotes != nil {
					t.Fatalf("旧价不能作为成功估值：%v，%v", quotes, err)
				}
				return
			}
			if err != nil || len(quotes) != 1 || quotes["09889.HK"].Price != tc.price || quotes["09889.HK"].Source != tc.source {
				t.Fatalf("主备来源选择错误：%+v，%v", quotes, err)
			}
		})
	}
}

func TestLatestHongKongCombinesValidQuotesWithoutPartialValuation(t *testing.T) {
	now := clock("2026-10-23 11:20:00")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/tencent" {
			fmt.Fprint(w, fixture("hk00700", "00700", "500", "2026/10/23 11:19:00")+
				fixture("hk09889", "09889", "123.17", "2026/10/21 16:00:00"))
			return
		}
		fmt.Fprintf(w, `{"rc":0,"data":{"diff":[{"f12":"09889","f13":116,"f2":3.2,"f3":1,"f5":100,"f6":320,"f124":%d}]}}`, clock("2026-10-23 11:19:00").Unix())
	}))
	defer server.Close()
	quotes, err := FetchLatest(context.Background(), server.Client(), server.URL+"/tencent?q=", server.URL+"/eastmoney", []string{"00700.HK", "09889.HK"}, now)
	if err != nil || quotes["00700.HK"].Price != 500 || quotes["09889.HK"].Price != 3.2 {
		t.Fatalf("每只持仓应独立使用通过校验的源：%v，%v", quotes, err)
	}
	quotes, err = FetchLatest(context.Background(), server.Client(), server.URL+"/tencent?q=", server.URL+"/eastmoney", []string{"00700.HK", "09889.HK", "00005.HK"}, now)
	if err == nil || quotes != nil {
		t.Fatal("任一持仓没有有效报价时，不得返回部分资产估值")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if quotes, err := FetchLatest(ctx, server.Client(), server.URL+"/tencent?q=", server.URL+"/eastmoney", []string{"00700.HK"}, now); err == nil || quotes != nil {
		t.Fatal("取消请求不能返回旧估值")
	}
}

func TestLatestHongKongRecoversFromProviderFailure(t *testing.T) {
	for _, broken := range []string{"/tencent", "/eastmoney"} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			if request.URL.Path == broken {
				http.Error(w, "unavailable", http.StatusBadGateway)
			} else if strings.HasPrefix(request.URL.Path, "/tencent") {
				fmt.Fprint(w, fixture("hk09889", "09889", "3.1", "2026/10/23 11:19:00"))
			} else {
				fmt.Fprintf(w, `{"rc":0,"data":{"diff":[{"f12":"09889","f13":116,"f2":3.2,"f3":1,"f5":100,"f6":320,"f124":%d}]}}`, clock("2026-10-23 11:19:00").Unix())
			}
		}))
		quotes, err := FetchLatest(context.Background(), server.Client(), server.URL+"/tencent?q=", server.URL+"/eastmoney", []string{"09889.HK"}, clock("2026-10-23 11:20:00"))
		server.Close()
		if err != nil || len(quotes) != 1 {
			t.Fatalf("单源故障应使用另一有效来源：%v，%v", quotes, err)
		}
	}
}

func TestLiveHongKongValuation(t *testing.T) {
	if os.Getenv("POSITION_QUOTE_LIVE_TEST") != "1" {
		t.Skip("设置 POSITION_QUOTE_LIVE_TEST=1 后只读验证公开港股估值")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 12*time.Second)
	defer cancel()
	quotes, err := FetchLatest(ctx, &http.Client{Timeout: 8 * time.Second}, "https://qt.gtimg.cn/q=", hkquote.EastmoneyURL, []string{"00700.HK", "09889.HK"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	for symbol, quote := range quotes {
		t.Logf("%s：价格%.3f，原始报价时间%d，来源%s", symbol, quote.Price, quote.At, quote.Source)
	}
}
