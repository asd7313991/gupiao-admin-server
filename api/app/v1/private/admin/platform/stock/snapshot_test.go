package stock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type snapshotTransport struct {
	base   *url.URL
	client http.RoundTripper
}

func (transport snapshotTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	request = request.Clone(request.Context())
	request.URL.Scheme, request.URL.Host = transport.base.Scheme, transport.base.Host
	return transport.client.RoundTrip(request)
}

func TestEastmoneySnapshotRefreshesWholeRanking(t *testing.T) {
	var round atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.URL.Query().Get("fid") != "f12" || !strings.Contains(request.URL.Query().Get("fields"), "f124") {
			t.Error("采集必须按代码稳定分页并请求原始报价时间")
		}
		page, _ := strconv.Atoi(request.URL.Query().Get("pn"))
		items := make([]map[string]any, 0, 100)
		for index := (page - 1) * 100; index < min(page*100, 201); index++ {
			rate := float64(201-index) / 100
			if round.Load() == 1 && index == 200 {
				rate = 20
			}
			items = append(items, map[string]any{
				"f12": fmt.Sprintf("%06d", 300000+index), "f13": 0, "f14": "测试证券",
				"f2": 10 + rate, "f3": rate, "f5": 100, "f6": 10000, "f8": 1,
				"f124": int64(1792726200) + int64(round.Load())*60,
			})
		}
		json.NewEncoder(w).Encode(map[string]any{"data": map[string]any{"total": 201, "diff": items}})
	}))
	defer server.Close()
	base, _ := url.Parse(server.URL)
	client := &http.Client{Transport: snapshotTransport{base, http.DefaultTransport}, Timeout: time.Second}
	first, err := fetchEastmoneySnapshot(client, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(first) != 201 || first[200].Code != "300200" || first[0].QuoteAt != 1792726200 {
		t.Fatalf("跨页采集及报价时间错误：条数=%d", len(first))
	}
	round.Store(1)
	second, err := fetchEastmoneySnapshot(client, 10000)
	if err != nil {
		t.Fatal(err)
	}
	sort.Slice(second, func(i, j int) bool { return second[i].ChangeRate > second[j].ChangeRate })
	if second[0].Code != "300200" || second[0].LastPrice != 30 || second[0].QuoteAt != 1792726260 {
		t.Fatalf("原来第3页的证券应进入新涨幅榜首位：%+v", second[0])
	}
	capped, err := fetchEastmoneySnapshot(client, 150)
	if err != nil || len(capped) != 150 {
		t.Fatalf("非整页采集上限错误：条数=%d，错误=%v", len(capped), err)
	}
}

func TestEastmoneySnapshotRejectsIncompleteSources(t *testing.T) {
	for _, body := range []string{
		`{"rc":1,"data":{"total":1,"diff":[{"f12":"300001","f2":10}]}}`,
		`{"data":null}`,
		`{"data":{"total":101,"diff":[{"f12":"300001","f2":10}]}}`,
		`{"data":{"total":2,"diff":[{"f12":"300001","f2":10},{"f12":"300001","f2":11}]}}`,
		`{"data":{"total":1,"diff":[{"f12":"abcdef","f2":10}]}}`,
		`{"data":{"total":1,"diff":[{"f12":"300001","f2":"NaN"}]}}`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			base, _ := url.Parse(server.URL)
			client := &http.Client{Transport: snapshotTransport{base, http.DefaultTransport}}
			if items, err := fetchEastmoneySnapshot(client, 10000); err == nil || items != nil {
				t.Fatalf("不完整或无效行情不能发布成成功快照：%v，%v", items, err)
			}
		})
	}
}

func TestTencentQuoteRequiresMatchingCodeAndTime(t *testing.T) {
	fields := make([]string, 33)
	fields[2], fields[3], fields[30], fields[32] = "300001", "10", "20261023113000", "1"
	for _, mutation := range []func(){
		func() { fields[2] = "300002" },
		func() { fields[2], fields[30] = "300001", "" },
		func() { fields[30], fields[3] = "20261023113000", "NaN" },
		func() { fields[3], fields[32] = "10", "NaN" },
	} {
		mutation()
		if len(parseTencentQuotes(`v_sz300001="`+strings.Join(fields, "~")+`";`)) != 0 {
			t.Fatal("代码、时间或数值无效时不应使用该报价")
		}
	}

}

func TestLiveEastmoneySnapshot(t *testing.T) {
	if os.Getenv("MARKET_QUOTE_LIVE_TEST") != "1" {
		t.Skip("设置 MARKET_QUOTE_LIVE_TEST=1 后读取公开全市场行情，不写数据库")
	}
	start := time.Now()
	items, err := fetchEastmoneySnapshot(&http.Client{Timeout: 12 * time.Second}, 10000)
	if err != nil {
		t.Fatal(err)
	}
	if len(items) < 1000 {
		t.Fatalf("全市场采集条数异常：%d", len(items))
	}
	count := 0
	for _, item := range items {
		if item.LastPrice > 0 && item.QuoteAt > 0 {
			count++
		}
	}
	if count < 1000 {
		t.Fatalf("有效价格和原始报价时间过少：%d", count)
	}
	t.Logf("公开行情采集：%d只，其中%d只有有效价格和时间；耗时%s", len(items), count, time.Since(start))
}
