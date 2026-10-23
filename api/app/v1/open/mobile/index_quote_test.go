package mobile

import (
	"strings"
	"testing"
	"time"
)

func TestIndicesUseOriginalQuoteTime(t *testing.T) {
	fields := make([]string, 38)
	fields[1], fields[2], fields[3] = "上证指数", "000001", "3614.12"
	fields[6], fields[30], fields[31], fields[32], fields[37] = "100", "20261023113000", "-43.91", "-1.20", "1000"
	line := func(symbol string) string { return `v_` + symbol + `="` + strings.Join(fields, "~") + `";` }
	indices := parseTencentIndices(line("sh000001") + line("sh000001"))
	wantTime := time.Date(2026, 10, 23, 11, 30, 0, 0, time.FixedZone("Asia/Shanghai", 8*60*60)).Unix()
	if len(indices) != 1 || indices[0].QuoteAt != wantTime || indices[0].Value != 3614.12 || indices[0].ChangeRate != -1.2 {
		t.Fatalf("必须使用完整报价、原始时间并去重：%+v", indices)
	}
	for _, mutate := range []func(){
		func() { fields[2] = "399001" },
		func() { fields[2], fields[30] = "000001", "" },
		func() { fields[30], fields[3] = "20261023113000", "NaN" },
		func() { fields[3], fields[32] = "3614.12", "bad" },
	} {
		mutate()
		if len(parseTencentIndices(line("sh000001"))) != 0 {
			t.Fatal("无效指数不能作为成功行情返回")
		}
	}
}
