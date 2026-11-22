package stock

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"api-server/db/pgdb/system"
)

func TestFillTencentPricesRefreshesStaleSTARAndBeijingQuotes(t *testing.T) {
	expected := expectedAShareQuoteTime(time.Now())
	quoteAt := expected - 30
	quoteTime := time.Unix(quoteAt, 0).In(time.FixedZone("CST", 8*60*60)).Format("20060102150405")
	requested := map[string]bool{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		for _, symbol := range strings.Split(strings.TrimPrefix(request.URL.Path, "/q="), ",") {
			requested[symbol] = true
			fields := make([]string, 39)
			fields[2], fields[3], fields[6], fields[30], fields[32], fields[37], fields[38] =
				symbol[2:], "10.5", "100", quoteTime, "5", "123.45", "2.5"
			fmt.Fprintf(w, `v_%s="%s";`, symbol, strings.Join(fields, "~"))
		}
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := &http.Client{Transport: snapshotTransport{base, http.DefaultTransport}, Timeout: time.Second}
	stale := expected - 6*60*60
	fresh := expected - 30
	securities := []system.StockSecurity{
		{Code: "688001", Exchange: "上交所", Board: "科创板", LastPrice: 10, QuoteAt: stale},
		{Code: "688002", Exchange: "上交所", Board: "科创板", LastPrice: 10, QuoteAt: fresh},
		{Code: "920001", Exchange: "北交所", Board: "北交所", LastPrice: 10, QuoteAt: stale},
		{Code: "920002", Exchange: "北交所", Board: "北交所", LastPrice: 10, QuoteAt: fresh},
		{Code: "600001", Exchange: "上交所", Board: "主板", LastPrice: 10, QuoteAt: stale},
		{Code: "600002", Exchange: "上交所", Board: "主板", LastPrice: 10, QuoteAt: fresh},
		{Code: "688003", Exchange: "上交所", Board: "科创板"},
	}
	if err := fillTencentPrices(client, securities); err != nil {
		t.Fatal(err)
	}
	if !requested["sh688001"] || !requested["bj920001"] || !requested["sh688003"] ||
		requested["sh688002"] || requested["bj920002"] || requested["sh600001"] {
		t.Fatalf("备用源只应刷新科创板、北交所中的旧报价及缺失报价：%v", requested)
	}
	for _, index := range []int{0, 2, 6} {
		if securities[index].QuoteAt != quoteAt || securities[index].LastPrice != 10.5 || securities[index].Source != "eastmoney/tencent" {
			t.Fatalf("旧报价未被较新的备用报价刷新：%+v", securities[index])
		}
	}
	for _, index := range []int{1, 3, 4, 5} {
		if securities[index].LastPrice != 10 || securities[index].Source != "" {
			t.Fatalf("新报价或其他板块不应被修改：%+v", securities[index])
		}
	}
}

func TestExpectedAShareQuoteTime(t *testing.T) {
	china := time.FixedZone("Asia/Shanghai", 8*60*60)
	tests := []struct {
		name string
		now  time.Time
		want time.Time
	}{
		{
			name: "before open uses previous weekday close",
			now:  time.Date(2026, 10, 26, 9, 0, 0, 0, china),
			want: time.Date(2026, 10, 23, 15, 0, 0, 0, china),
		},
		{
			name: "lunch break uses morning close",
			now:  time.Date(2026, 10, 26, 12, 0, 0, 0, china),
			want: time.Date(2026, 10, 26, 11, 30, 0, 0, china),
		},
		{
			name: "after close uses same day close",
			now:  time.Date(2026, 10, 26, 16, 0, 0, 0, china),
			want: time.Date(2026, 10, 26, 15, 0, 0, 0, china),
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := expectedAShareQuoteTime(test.now); got != test.want.Unix() {
				t.Fatalf("expected quote time %s, got %s", test.want, time.Unix(got, 0).In(china))
			}
		})
	}
}
