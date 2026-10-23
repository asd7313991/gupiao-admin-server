package stock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func priorityTestTime() time.Time {
	location := time.FixedZone("UTC+8", 8*3600)
	current := time.Now().In(location)
	at := time.Date(current.Year(), current.Month(), current.Day(), 13, 30, 0, 0, location)
	if at.After(current) {
		at = at.AddDate(0, 0, -1)
	}
	for at.Weekday() == time.Saturday || at.Weekday() == time.Sunday {
		at = at.AddDate(0, 0, -1)
	}
	return at
}

func priorityTencentFixture(symbol string, at time.Time) string {
	if strings.HasSuffix(symbol, ".HK") {
		return hongKongQuoteFixture(strings.Split(symbol, ".")[0], "7", at.Format("2006/01/02 15:04:05"))
	}
	fields := make([]string, 45)
	fields[2], fields[3], fields[6], fields[30], fields[32], fields[37], fields[38] =
		strings.Split(symbol, ".")[0], "7", "100", at.Format("20060102150405"), "2", "10000", "1"
	return "v_" + strings.ToLower(strings.Split(symbol, ".")[1]) + fields[2] + `="` + strings.Join(fields, "~") + `";`
}

func TestPriorityQuotesChooseNewestValidSource(t *testing.T) {
	now := priorityTestTime()
	for _, symbol := range []string{"600519.SH", "002384.SZ", "920001.BJ", "00700.HK"} {
		for _, tc := range []struct {
			name, broken, source string
			tencentAge, emAge    time.Duration
			price                float64
		}{
			{"东方财富较新", "", "eastmoney", time.Minute, 30 * time.Second, 8},
			{"腾讯较新", "", "tencent", 30 * time.Second, time.Minute, 7},
			{"同时间东方财富优先", "", "eastmoney", time.Minute, time.Minute, 8},
			{"腾讯失败切换东方财富", "/tencent", "eastmoney", time.Minute, time.Minute, 8},
			{"东方财富失败切换腾讯", "/eastmoney", "tencent", time.Minute, time.Minute, 7},
			{"腾讯旧价使用东方财富", "", "eastmoney", 31 * time.Minute, time.Minute, 8},
			{"东方财富旧价使用腾讯", "", "tencent", time.Minute, 31 * time.Minute, 7},
		} {
			t.Run(symbol+"/"+tc.name, func(t *testing.T) {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path == tc.broken {
						http.Error(w, "unavailable", http.StatusBadGateway)
					} else if r.URL.Path == "/tencent" {
						fmt.Fprint(w, priorityTencentFixture(symbol, now.Add(-tc.tencentAge)))
					} else {
						parts := strings.Split(r.URL.Query().Get("secids"), ".")
						market, _ := strconv.Atoi(parts[0])
						json.NewEncoder(w).Encode(map[string]any{"rc": 0, "data": map[string]any{"diff": []map[string]any{
							{"f12": parts[1], "f13": market, "f2": 8, "f3": 3, "f5": 100, "f6": 800, "f124": now.Add(-tc.emAge).Unix()},
						}}})
					}
				}))
				defer server.Close()
				item := system.StockSecurity{Code: strings.Split(symbol, ".")[0], Symbol: symbol, Status: system.StatusDisabled,
					LastPrice: 123.17, QuoteAt: 1, Board: "人工设置"}
				rows, missing, sourceErrors := fetchPriorityBatch(context.Background(), server.Client(), server.URL+"/tencent", server.URL+"/eastmoney", []system.StockSecurity{item}, now)
				if len(rows) != 1 || len(missing) != 0 || rows[0].LastPrice != tc.price || rows[0].Source != tc.source ||
					rows[0].QuoteAt == now.Unix() || rows[0].QuoteAt <= 1 || rows[0].Status != item.Status || rows[0].Board != item.Board {
					t.Fatalf("主备择新、保留原始时间或人工状态错误：%+v，%v", rows, missing)
				}
				if (tc.broken != "") != (len(sourceErrors) > 0) {
					t.Fatalf("源端失败必须可观测：%v", sourceErrors)
				}
				if item.LastPrice != 123.17 || item.QuoteAt != 1 {
					t.Fatal("取价不得修改输入旧快照")
				}
			})
		}
	}
}

func TestPriorityQuotesKeepOtherStocksWhenOneIsStale(t *testing.T) {
	now := priorityTestTime()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/tencent" {
			fmt.Fprint(w, priorityTencentFixture("00700.HK", now.Add(-time.Minute))+
				priorityTencentFixture("00875.HK", now.Add(-31*time.Minute)))
		} else {
			fmt.Fprintf(w, `{"rc":0,"data":{"diff":[{"f12":"00875","f13":116,"f2":0.335,"f3":0,"f5":100,"f6":33.5,"f124":%d}]}}`, now.Add(-31*time.Minute).Unix())
		}
	}))
	defer server.Close()
	items := []system.StockSecurity{{Code: "00700", Symbol: "00700.HK"}, {Code: "00875", Symbol: "00875.HK"}}
	valid, missing, _ := fetchPriorityBatch(context.Background(), server.Client(), server.URL+"/tencent", server.URL+"/eastmoney", items, now)
	if len(valid) != 1 || valid[0].Symbol != "00700.HK" || len(missing) != 1 || missing[0] != "00875.HK" {
		t.Fatalf("单只旧价只能被记录为异常，不得阻断其他重点股票：%+v，%v", valid, missing)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	valid, missing, sourceErrors := fetchPriorityBatch(ctx, server.Client(), server.URL+"/tencent", server.URL+"/eastmoney", items, now)
	if len(valid) != 0 || len(missing) != 2 || len(sourceErrors) != 2 {
		t.Fatal("取消或双源失败不得发布旧快照")
	}
}

func TestQuoteWritesUseStableOrder(t *testing.T) {
	items := []system.StockSecurity{{Code: "900001"}, {Code: "000001"}, {Code: "300001"}}
	ordered := orderedQuoteRows(items)
	if ordered[0].Code != "000001" || ordered[1].Code != "300001" || ordered[2].Code != "900001" || items[0].Code != "900001" {
		t.Fatal("轮转和全市场写入应统一按代码加锁，避免交叉死锁且不修改输入")
	}
}

func TestPriorityRotationAndBackoff(t *testing.T) {
	items := make([]system.StockSecurity, 251)
	for index := range items {
		items[index].Symbol = fmt.Sprintf("%06d.SZ", index+1)
	}
	seen, cursor := make(map[string]bool), ""
	for round := 0; round < 3; round++ {
		batch := priorityBatch(items, cursor)
		if len(batch) != 100 {
			t.Fatalf("每市场每轮必须限制为100只，实际%d", len(batch))
		}
		for _, item := range batch {
			seen[item.Symbol] = true
		}
		cursor = batch[len(batch)-1].Symbol
	}
	if len(seen) != len(items) {
		t.Fatal("超量重点股票必须轮转覆盖，不得一直截取前100只")
	}
	for failures, seconds := range []int{5, 5, 10, 20, 40, 60, 60} {
		if priorityDelay(5, failures) != time.Duration(seconds)*time.Second {
			t.Fatalf("失败%d次退避应为%d秒", failures, seconds)
		}
	}
	if priorityDelay(60, 100) != time.Minute {
		t.Fatal("盘外及连续失败间隔不得超过60秒上限")
	}
}

func TestPriorityTargetScopeAndActiveExpiry(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=127.0.0.1 user=test dbname=test sslmode=disable"),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, market := range []string{"A", "HK"} {
		var items []system.StockSecurity
		result := priorityTargets(db, market, []string{"00700.HK", "002384.SZ"}).Find(&items)
		sql := result.Statement.SQL.String()
		for _, required := range []string{"trade_positions", "position_qty > 0", "customer_watchlists", "symbol IN", "deleted_at IS NULL", "ORDER BY symbol"} {
			if result.Error != nil || !strings.Contains(sql, required) {
				t.Fatalf("重点范围或市场过滤不完整：%s，%v", sql, result.Error)
			}
		}
		if market == "HK" && result.Statement.Vars[0] != "%.HK" {
			t.Fatal("港股任务不得混入A股")
		}
	}
	priorityActive.Lock()
	previous := priorityActive.items
	now := time.Now()
	priorityActive.items = map[string]time.Time{"00700.HK": now.Add(-121 * time.Second), "002384.SZ": now.Add(-10 * time.Second)}
	priorityActive.Unlock()
	defer func() {
		priorityActive.Lock()
		priorityActive.items = previous
		priorityActive.Unlock()
	}()
	TouchPrioritySecurity("09889.HK")
	active := activePrioritySymbols(now)
	sort.Strings(active)
	if strings.Join(active, ",") != "002384.SZ,09889.HK" {
		t.Fatalf("近期查看窗口应为2分钟，实际%v", active)
	}
}

func TestPriorityAlertsAndDelayedBoundary(t *testing.T) {
	now := priorityTestTime()
	for _, tc := range []struct {
		seconds int64
		delayed bool
	}{{180, false}, {181, true}} {
		if priorityQuoteDelayed(system.StockSecurity{Symbol: "00700.HK", QuoteAt: now.Unix() - tc.seconds}, now) != tc.delayed {
			t.Fatal("延迟提示应精确检查3分钟边界，且不放宽30分钟交易校验")
		}
	}
	if priorityStalled(PriorityQuoteStatus{State: "success", LastAttempt: now.Unix(), NextAttempt: now.Unix() + 5}, now.Add(50*time.Second)) ||
		!priorityStalled(PriorityQuoteStatus{State: "success", LastAttempt: now.Unix(), NextAttempt: now.Unix() + 5}, now.Add(51*time.Second)) ||
		priorityStalled(PriorityQuoteStatus{State: "disabled"}, now.Add(time.Hour)) {
		t.Fatal("任务停滞应在下次计划后45秒检查，关闭同步不误报停滞")
	}
	job := priorityJobs["HK"]
	job.Lock()
	previous, warnings := job.status, job.warnings
	job.status = PriorityQuoteStatus{Market: "HK", State: "degraded", LastAttempt: time.Now().Unix(), NextAttempt: time.Now().Add(5 * time.Second).Unix()}
	job.warnings = map[string]string{"00875.HK": "00875.HK 主备源均无有效新报价"}
	job.Unlock()
	defer func() {
		job.Lock()
		job.status, job.warnings = previous, warnings
		job.Unlock()
	}()
	if PriorityWarning("00875.HK") == "" || PriorityWarning("00700.HK") != "" {
		t.Fatal("单只股票异常不得误报到其他股票")
	}
	body, err := json.Marshal(PriorityStatuses(time.Now()))
	if err != nil || strings.Contains(string(body), `"source_errors":null`) || strings.Contains(string(body), `"symbols":null`) {
		t.Fatalf("前台告警列表必须返回数组而不是null：%s，%v", body, err)
	}
	job.Lock()
	delete(job.warnings, "00875.HK")
	job.status.State = "success"
	job.Unlock()
	if PriorityWarning("00875.HK") != "" {
		t.Fatal("恢复有效报价后应清除该股票告警")
	}
}
