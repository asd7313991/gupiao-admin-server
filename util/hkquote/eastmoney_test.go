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
