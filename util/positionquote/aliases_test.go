package positionquote

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

func TestTencentHongKongCandidatesKeepOriginalTime(t *testing.T) {
	if !reflect.DeepEqual(TencentCodes("00875.HK"), []string{"r_hk00875", "hk00875"}) ||
		!reflect.DeepEqual(TencentCodes("002384.SZ"), []string{"sz002384"}) || TencentCodes("875.HK") != nil {
		t.Fatal("港股双候选须保留前导零，A股及无效代码规则不变")
	}
	now := clock("2026-10-23 15:20:00")
	fresh := fixture("r_hk00875", "00875", "0.340", "2026/10/23 15:08:09")
	old := fixture("hk00875", "00875", "0.335", "2026/10/23 10:46:46")
	requested := map[string]string{"r_hk00875": "00875.HK", "hk00875": "00875.HK"}
	for _, body := range []string{fresh + old, old + fresh, fresh} {
		quotes, err := Parse(body, requested, now)
		if err != nil || quotes["00875.HK"].Price != 0.340 || quotes["00875.HK"].At != clock("2026-10-23 15:08:09").Unix() {
			t.Fatalf("较新候选须恢复有效报价，不能使用本机时间：%+v %v", quotes, err)
		}
	}
	regular := fixture("hk00875", "00875", "0.345", "2026/10/23 15:19:00")
	for _, candidate := range []string{strings.Replace(old, "v_hk", "v_r_hk", 1), `v_r_hk00875="bad";`,
		fixture("r_hk00875", "00875", "1", "2026/10/23 15:21:01"), fresh + fresh} {
		quotes, err := Parse(candidate+regular, requested, now)
		if err != nil || quotes["00875.HK"].Price != 0.345 {
			t.Fatalf("候选失效时保留另一有效候选：%+v %v", quotes, err)
		}
	}
}

func TestAvailableQuotesDoNotChangeStrictValuation(t *testing.T) {
	now := clock("2026-10-23 15:20:00")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/eastmoney" {
			fmt.Fprint(w, `{"rc":0,"data":{"diff":[]}}`)
			return
		}
		if !strings.Contains(r.URL.Query().Get("q"), "r_hk00875") {
			t.Error("估值请求必须包含港股新候选")
		}
		fmt.Fprint(w, fixture("r_hk00700", "00700", "388", "2026/10/23 15:19:00")+
			fixture("hk00875", "00875", "0.335", "2026/10/23 10:46:46"))
	}))
	defer server.Close()
	symbols := []string{"00700.HK", "00875.HK", "bad-symbol"}
	quotes, err := FetchAvailable(context.Background(), server.Client(), server.URL+"/tencent?q=", server.URL+"/eastmoney", symbols, now)
	if err == nil || len(quotes) != 1 || quotes["00700.HK"].Price != 388 || !strings.Contains(err.Error(), "00875.HK") {
		t.Fatalf("逐只估值须保留正常股票并明确报告缺失：%+v %v", quotes, err)
	}
	quotes, err = FetchLatest(context.Background(), server.Client(), server.URL+"/tencent?q=", server.URL+"/eastmoney", symbols, now)
	if err == nil || quotes != nil {
		t.Fatal("完整资产接口仍不得使用部分估值冒充总额")
	}
}
