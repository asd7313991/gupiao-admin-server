package stock

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"api-server/db/pgdb/system"
)

func TestTencentSnapshotUpdatesAllFieldsAndMarksMissing(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		list := strings.TrimPrefix(request.URL.Path, "/q=")
		for _, symbol := range strings.Split(list, ",") {
			if symbol == "sz300200" {
				continue
			}
			fields := make([]string, 39)
			fields[2], fields[3], fields[6], fields[30], fields[32], fields[37], fields[38] = symbol[2:], "10.5", "100", "20261023113000", "5", "123.45", "2.5"
			fmt.Fprintf(w, `v_%s="%s";`, symbol, strings.Join(fields, "~"))
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := &http.Client{Transport: snapshotTransport{base, http.DefaultTransport}}
	existing := make([]system.StockSecurity, 201)
	for index := range existing {
		existing[index] = system.StockSecurity{Code: fmt.Sprintf("%06d", 300000+index), Exchange: "深交所", LastPrice: 123.17, ChangeRate: 80, Amount: 999, QuoteAt: 123}
	}
	items, err := fetchTencentSnapshot(client, existing)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 201 || items[199].LastPrice != 10.5 || items[199].ChangeRate != 5 || items[199].Volume != 100 || items[199].Amount != 1234500 || items[199].Turnover != 2.5 || items[199].Source != "tencent" || items[199].QuoteAt <= 123 {
		t.Fatalf("备用源须更新全市场全部行情字段：%+v", items[199])
	}
	if items[200].LastPrice != 0 || items[200].QuoteAt != 0 || items[200].Source != "tencent/unavailable" {
		t.Fatalf("缺失报价不得冒用旧价格或旧时间：%+v", items[200])
	}
	if existing[0].LastPrice != 123.17 {
		t.Fatal("采集不得修改输入的旧快照")
	}
}

func TestTencentSnapshotFailsExplicitly(t *testing.T) {
	for _, status := range []int{http.StatusBadGateway, http.StatusOK} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
			w.WriteHeader(status)
		}))
		base, _ := url.Parse(server.URL)
		client := &http.Client{Transport: snapshotTransport{base, http.DefaultTransport}}
		items, err := fetchTencentSnapshot(client, []system.StockSecurity{{Code: "300001", Exchange: "深交所", LastPrice: 123}})
		server.Close()
		if err == nil || items != nil {
			t.Fatal("备用源不可用时不能返回旧价成功快照")
		}
	}
}

func TestLiveTencentMarketSnapshot(t *testing.T) {
	if os.Getenv("MARKET_QUOTE_LIVE_TEST") != "1" {
		t.Skip("设置 MARKET_QUOTE_LIVE_TEST=1 后读取公开备用报价，不写数据库")
	}
	items, err := fetchTencentSnapshot(&http.Client{Timeout: 12 * time.Second}, []system.StockSecurity{
		{Code: "002384", Exchange: "深交所"},
		{Code: "300584", Exchange: "深交所"},
		{Code: "920285", Exchange: "北交所"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, item := range items {
		if item.LastPrice <= 0 || item.QuoteAt <= 0 || item.Amount <= 0 || item.Volume <= 0 || item.Source != "tencent" {
			t.Fatalf("备用报价无效：%+v", item)
		}
		t.Logf("%s：价格%.2f，涨跌幅%.2f%%，原始报价时间%d", item.Code, item.LastPrice, item.ChangeRate, item.QuoteAt)
	}
}
