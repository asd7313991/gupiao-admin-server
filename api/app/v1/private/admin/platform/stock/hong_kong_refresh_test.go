package stock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"api-server/db/pgdb/system"
)

func TestHongKongRefreshUpdatesWholeMarketWithoutCachedPrices(t *testing.T) {
	var round atomic.Int32
	at := time.Now().Add(-time.Minute).Truncate(time.Second)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/tencent" {
			for _, symbol := range strings.Split(request.URL.Query().Get("q"), ",") {
				code := strings.TrimPrefix(strings.TrimPrefix(symbol, "r_"), "hk")
				price, rate := "2", "0.1"
				if round.Load() == 1 && code == "00601" {
					price, rate = "1000", "20"
				}
				quoteAt := at.Add(-time.Second)
				if round.Load() == 1 {
					quoteAt = at.Add(15 * time.Second)
				}
				body := hongKongQuoteFixture(code, price, quoteAt.In(hkLocation).Format("2006/01/02 15:04:05"))
				body = strings.Replace(body, "v_hk"+code, "v_"+symbol, 1)
				fmt.Fprint(w, strings.Replace(body, "-1.77", rate, 1))
			}
			return
		}
		items := make([]map[string]any, 0)
		for _, secid := range strings.Split(request.URL.Query().Get("secids"), ",") {
			items = append(items, map[string]any{"f12": strings.TrimPrefix(secid, "116."), "f13": 116, "f2": 3, "f3": 1, "f5": 100, "f6": 300, "f124": at.Unix() + int64(round.Load())*10})
		}
		json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"diff": items}})
	}))
	defer server.Close()
	existing := make([]system.StockSecurity, 601)
	for index := range existing {
		code := fmt.Sprintf("%05d", index+1)
		existing[index] = system.StockSecurity{Code: code, Symbol: code + ".HK", Name: "测试港股", Market: "港股", Status: system.StatusDisabled, LastPrice: 123.17, QuoteAt: at.Add(-48 * time.Hour).Unix()}
	}
	first, err := refreshHongKongSnapshot(context.Background(), server.Client(), server.URL+"/tencent", server.URL+"/eastmoney", existing)
	if err != nil || len(first) != 601 || first[600].LastPrice != 3 || first[600].Source != "eastmoney" {
		t.Fatalf("第一轮全市场报价错误：条数%d，%v", len(first), err)
	}
	round.Store(1)
	second, err := refreshHongKongSnapshot(context.Background(), server.Client(), server.URL+"/tencent", server.URL+"/eastmoney", existing)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(second, func(i, j int) bool { return second[i].ChangeRate > second[j].ChangeRate })
	if second[0].Code != "00601" || second[0].LastPrice != 1000 || second[0].Source != "sina/tencent" || second[0].QuoteAt != at.Add(15*time.Second).Unix() {
		t.Fatalf("旧末页股票须可进入新榜首：%+v", second[0])
	}
	if second[0].Status != system.StatusDisabled || existing[600].LastPrice != 123.17 {
		t.Fatal("刷新报价不能改变启停状态或修改旧快照")
	}
}

func TestHongKongRefreshDoesNotPublishProviderFailures(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		http.Error(w, "unavailable", http.StatusBadGateway)
	}))
	defer server.Close()
	items, err := refreshHongKongSnapshot(context.Background(), server.Client(), server.URL, server.URL, []system.StockSecurity{{Code: "00700", LastPrice: 123.17, QuoteAt: 1}})
	if err == nil || items != nil {
		t.Fatal("主备故障不得将缓存旧价发布为成功快照")
	}
}
