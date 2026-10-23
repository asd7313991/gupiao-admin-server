package stock

import (
	"strings"
	"testing"
	"time"
)

func TestHongKongSnapshotCandidatesChooseNewestAndIsolateBadRows(t *testing.T) {
	now := time.Now().In(hkLocation).Add(-time.Minute)
	regular := hongKongQuoteFixture("00875", "0.335", now.Add(-4*time.Hour).Format("2006/01/02 15:04:05"))
	recent := strings.Replace(hongKongQuoteFixture("00875", "0.34", now.Format("2006/01/02 15:04:05")), "v_hk", "v_r_hk", 1)
	for _, body := range []string{regular + recent, recent + regular} {
		quotes, err := parseHongKongTencentQuotes(body)
		if err != nil || quotes["00875"].LastPrice != 0.34 || quotes["00875"].QuoteAt != now.Unix() {
			t.Fatalf("全市场和重点同步须选择较新的腾讯候选：%+v %v", quotes, err)
		}
	}
	quotes, err := parseHongKongTencentQuotes(`v_r_hk00700="bad";` + recent)
	if err == nil || quotes["00875"].LastPrice != 0.34 {
		t.Fatalf("单只候选异常不得丢弃其他股票，且错误必须明确：%+v %v", quotes, err)
	}
}
