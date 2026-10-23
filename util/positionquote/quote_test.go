package positionquote

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func clock(value string) time.Time {
	at, err := time.ParseInLocation("2006-01-02 15:04:05", value, time.FixedZone("UTC+8", 8*3600))
	if err != nil {
		panic(err)
	}
	return at
}

func fixture(symbol, code, price, at string) string {
	fields := make([]string, 33)
	fields[2], fields[3], fields[30] = code, price, at
	return `v_` + symbol + `="` + strings.Join(fields, "~") + `";`
}

func TestValidateTime(t *testing.T) {
	for _, tc := range []struct {
		name, now, at string
		hk, valid     bool
	}{
		{"盘中新报价", "2026-10-23 11:20:00", "2026-10-23 11:19:00", false, true},
		{"30分钟边界", "2026-10-23 11:20:00", "2026-10-23 10:50:00", false, true},
		{"超过30分钟", "2026-10-23 11:20:01", "2026-10-23 10:50:00", false, false},
		{"拒绝截图旧价", "2026-10-23 11:20:00", "2026-10-21 16:37:12", false, false},
		{"拒绝超前报价", "2026-10-23 11:20:00", "2026-10-23 11:21:01", false, false},
		{"午休保留上午报价", "2026-10-23 12:50:00", "2026-10-23 11:25:00", false, true},
		{"午休拒绝昨日报价", "2026-10-23 12:50:00", "2026-10-22 15:00:00", false, false},
		{"下午开市不延长上午时效", "2026-10-23 13:00:00", "2026-10-23 11:25:00", false, false},
		{"盘后收市价", "2026-10-23 20:00:00", "2026-10-23 15:00:00", false, true},
		{"盘后不接受上午价", "2026-10-23 20:00:00", "2026-10-23 11:20:00", false, false},
		{"周末用周五收市", "2026-10-25 12:00:00", "2026-10-23 15:00:00", false, true},
		{"周一开盘前", "2026-10-26 08:00:00", "2026-10-23 15:00:00", false, true},
		{"周末拒绝周四", "2026-10-25 12:00:00", "2026-10-22 15:00:00", false, false},
		{"港股午间仍在交易", "2026-10-23 11:50:00", "2026-10-23 11:10:00", true, false},
		{"港股午休", "2026-10-23 12:50:00", "2026-10-23 11:55:00", true, true},
		{"港股周末收市", "2026-10-25 12:00:00", "2026-10-23 16:08:00", true, true},
		{"港股午休拒绝昨日报价", "2026-10-23 12:50:00", "2026-10-22 16:00:00", true, false},
		{"港股午休拒绝过早上午价", "2026-10-23 12:50:00", "2026-10-23 11:29:59", true, false},
		{"港股下午开市拒绝上午旧价", "2026-10-23 13:00:00", "2026-10-23 11:55:00", true, false},
		{"港股15点后仍检查盘中时效", "2026-10-23 15:31:00", "2026-10-23 15:00:00", true, false},
		{"港股16点前收市阶段边界", "2026-10-23 20:00:00", "2026-10-23 15:30:00", true, true},
		{"港股盘后不能用15点旧价", "2026-10-23 20:00:00", "2026-10-23 15:00:00", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateTime(clock(tc.at).Unix(), clock(tc.now), tc.hk)
			if (err == nil) != tc.valid {
				t.Fatalf("valid=%v err=%v", tc.valid, err)
			}
		})
	}
}

func TestParse(t *testing.T) {
	now := clock("2026-10-23 11:20:00")
	requested := map[string]string{"sz002384": "002384.SZ", "hk09889": "09889.HK"}
	a := fixture("sz002384", "002384", "106.24", "20261023111900")
	hk := fixture("hk09889", "09889", "3.15", "2026/10/23 11:18:00")
	quotes, err := Parse("\n "+a+"\n "+hk, requested, now)
	if err != nil || len(quotes) != 2 || quotes["002384.SZ"].Price != 106.24 || quotes["09889.HK"].At != clock("2026-10-23 11:18:00").Unix() {
		t.Fatalf("报价解析错误：%+v %v", quotes, err)
	}
	for _, body := range []string{
		"", "<html>error</html>", a,
		strings.Replace(a, "106.24", "NaN", 1) + hk,
		strings.Replace(a, "106.24", "0", 1) + hk,
		strings.Replace(a, "20261023111900", "20261021163712", 1) + hk,
		strings.Replace(a, "~002384~", "~002385~", 1) + hk,
		a + a + hk,
	} {
		if _, err := Parse(body, requested, now); err == nil {
			t.Fatalf("应拒绝无效、不完整或旧报价：%s", body)
		}
	}
}

func TestFetch(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if r.URL.Query().Get("q") != "sz002384" {
			t.Error("未去重或证券代码错误")
		}
		fmt.Fprint(w, fixture("sz002384", "002384", "106.24", "20261023111900"))
	}))
	defer server.Close()
	quotes, err := Fetch(context.Background(), server.Client(), server.URL+"?q=", []string{"002384.SZ", "002384.SZ"}, clock("2026-10-23 11:20:00"))
	if err != nil || calls != 1 || len(quotes) != 1 {
		t.Fatalf("batch=%d quotes=%v err=%v", calls, quotes, err)
	}
	if quotes, err := Fetch(context.Background(), server.Client(), server.URL, nil, time.Now()); err != nil || len(quotes) != 0 || calls != 1 {
		t.Fatal("空持仓不应请求行情")
	}
	for _, symbol := range []string{"002384", "2384.SZ", "002384.HK", "ab2384.SZ", "002384.XX"} {
		if _, err := Fetch(context.Background(), server.Client(), server.URL, []string{symbol}, time.Now()); err == nil {
			t.Fatalf("应拒绝证券代码：%s", symbol)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Fetch(ctx, server.Client(), server.URL, []string{"002384.SZ"}, time.Now()); err == nil {
		t.Fatal("取消请求不得返回旧估值")
	}
}

func TestLivePositionQuote(t *testing.T) {
	if os.Getenv("POSITION_QUOTE_LIVE_TEST") != "1" {
		t.Skip("未开启公开持仓报价联网测试")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	quotes, err := Fetch(ctx, &http.Client{Timeout: 8 * time.Second}, "https://qt.gtimg.cn/q=", []string{"002384.SZ"}, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	quote := quotes["002384.SZ"]
	t.Logf("东山精密公开估值价格=%.2f，原始报价时间=%d", quote.Price, quote.At)
}
