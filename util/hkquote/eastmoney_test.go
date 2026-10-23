package hkquote

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFetchEastmoney(t *testing.T) {
	at := time.Now().Add(-15 * time.Minute).Unix()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("secids") != "116.00700,116.09889" || r.URL.Query().Get("fltt") != "2" ||
			r.URL.Query().Get("fields") != "f12,f13,f2,f3,f5,f6,f124" {
			t.Error("港股代码、精度或字段参数错误")
		}
		fmt.Fprintf(w, `{"rc":0,"data":{"diff":[{"f12":"00700","f13":116,"f2":382.405,"f3":-2.89,"f5":60791747,"f6":23240805376,"f124":%d},{"f12":"09889","f13":116,"f2":"-","f3":"-","f5":0,"f6":0,"f124":0}]}}`, at)
	}))
	defer server.Close()
	quotes, err := FetchEastmoney(context.Background(), server.Client(), server.URL, []string{"00700", "09889"})
	if err != nil || len(quotes) != 1 || quotes["00700"].LastPrice != 382.405 || quotes["00700"].QuoteAt != at ||
		quotes["00700"].Volume != 60791747 || quotes["00700"].Amount != 23240805376 {
		t.Fatalf("报价或原始时间解析错误：%+v %v", quotes, err)
	}
	for _, codes := range [][]string{nil, {"700"}, {"00700", "00700"}, {"ab700"}} {
		if _, err := FetchEastmoney(context.Background(), server.Client(), server.URL, codes); err == nil {
			t.Fatalf("应拒绝代码：%v", codes)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := FetchEastmoney(ctx, server.Client(), server.URL, []string{"00700"}); err == nil {
		t.Fatal("取消请求不得返回成功")
	}
}

func TestParseEastmoneyRejectsInvalidData(t *testing.T) {
	now := time.Now()
	raw := fmt.Sprintf(`{"rc":0,"data":{"diff":[{"f12":"00700","f13":116,"f2":382.4,"f3":-2.89,"f5":100,"f6":38240,"f124":%d}]}}`, now.Unix())
	for _, body := range []string{
		`<html>error</html>`, `{}`, `{"rc":1,"data":null}`, `{"rc":0,"data":{}}`,
		strings.Replace(raw, `"00700"`, `"09988"`, 1),
		strings.Replace(raw, `"f13":116`, `"f13":1`, 1),
		strings.Replace(raw, `"f2":382.4`, `"f2":"NaN"`, 1),
		strings.Replace(raw, `"f5":100`, `"f5":"invalid"`, 1),
	} {
		if _, err := parseEastmoney([]byte(body), map[string]bool{"00700": true}, now); err == nil {
			t.Fatalf("应拒绝无效响应：%s", body)
		}
	}
	for _, replacement := range [][2]string{
		{`"f2":382.4`, `"f2":0`},
		{`"f2":382.4`, `"f2":1000001`},
		{`"f5":100`, `"f5":-1`},
		{fmt.Sprintf(`"f124":%d`, now.Unix()), fmt.Sprintf(`"f124":%d`, now.Add(61*time.Second).Unix())},
		{fmt.Sprintf(`"f124":%d`, now.Unix()), `"f124":null`},
	} {
		body := strings.Replace(raw, replacement[0], replacement[1], 1)
		quotes, err := parseEastmoney([]byte(body), map[string]bool{"00700": true}, now)
		if err != nil || len(quotes) != 0 {
			t.Fatalf("无效数值不得成为可用报价：%s %+v %v", body, quotes, err)
		}
	}
}

func TestFetchEastmoneyHTTPFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer server.Close()
	if _, err := FetchEastmoney(context.Background(), server.Client(), server.URL, []string{"00700"}); err == nil {
		t.Fatal("行情接口失败不得返回成功")
	}
}

func TestEastmoneySymbolsRejectsMissingMarket(t *testing.T) {
	now := time.Now()
	for _, market := range []string{"", `"f13":1,`} {
		body := fmt.Sprintf(`{"rc":0,"data":{"diff":[{"f12":"002384",%s"f2":112.3,"f3":0,"f5":100,"f6":11230,"f124":%d}]}}`, market, now.Unix())
		if _, err := parseEastmoneyMarkets([]byte(body), map[string]bool{"002384": true}, map[string]int{"002384": 0}, now); err == nil {
			t.Fatal("深市市场字段缺失不能被默认为0，错误市场也必须拒绝")
		}
	}
}

func TestFetchEastmoneySymbolsUsesActualMarkets(t *testing.T) {
	at := time.Now().Add(-time.Minute).Unix()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("secids") != "1.600519,0.002384,0.920001,116.00700" {
			t.Errorf("A股/港股主备代码或市场映射错误：%s", r.URL.RawQuery)
		}
		fmt.Fprintf(w, `{"rc":0,"data":{"diff":[
				{"f12":"600519","f13":1,"f2":100,"f3":1,"f5":100,"f6":10000,"f124":%d},
				{"f12":"002384","f13":0,"f2":112.3,"f3":2,"f5":200,"f6":22460,"f124":%d},
				{"f12":"920001","f13":0,"f2":20,"f3":-1,"f5":300,"f6":6000,"f124":%d},
				{"f12":"00700","f13":116,"f2":500,"f3":3,"f5":400,"f6":200000,"f124":%d}]}}`, at, at, at, at)
	}))
	defer server.Close()
	symbols := []string{"600519.SH", "002384.SZ", "920001.BJ", "00700.HK"}
	quotes, err := FetchEastmoneySymbols(context.Background(), server.Client(), server.URL, symbols)
	if err != nil || len(quotes) != 4 || quotes["002384.SZ"].LastPrice != 112.3 || quotes["00700.HK"].QuoteAt != at {
		t.Fatalf("A股/港股报价及原始时间错误：%v，%v", quotes, err)
	}
	for _, symbols := range [][]string{nil, {"600519.US"}, {"002384.SZ", "002384.SZ"}, {"ABCDEF.SH"}, {"700.HK"}} {
		if _, err := FetchEastmoneySymbols(context.Background(), server.Client(), server.URL, symbols); err == nil {
			t.Fatalf("应拒绝无效证券代码：%v", symbols)
		}
	}
}
