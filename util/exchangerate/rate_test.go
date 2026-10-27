package exchangerate

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"api-server/db/pgdb/system"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func clock(value string) time.Time {
	now, err := time.ParseInLocation("2006-01-02 15:04", value, Location)
	if err != nil {
		panic(err)
	}
	return now
}

func TestExpectedDate(t *testing.T) {
	for _, tc := range []struct{ now, date string }{
		{"2026-10-26 09:14", "2026-10-23"},
		{"2026-10-26 09:15", "2026-10-26"},
		{"2026-10-24 14:00", "2026-10-23"},
		{"2026-10-25 08:00", "2026-10-23"},
		{"2026-10-05 14:00", "2026-09-30"},
		{"2026-10-08 09:14", "2026-09-30"},
		{"2026-10-08 09:15", "2026-10-08"},
		{"2026-10-10 14:00", "2026-10-09"},
		{"2026-02-20 14:00", "2026-02-13"},
		{"2026-01-01 14:00", "2025-12-31"},
		{"2027-01-04 14:00", "2027-01-04"},
	} {
		if got := ExpectedDate(clock(tc.now)); got != tc.date {
			t.Fatalf("%s 应使用%s，实际%s", tc.now, tc.date, got)
		}
	}
}

func TestValidateDailyRate(t *testing.T) {
	now := clock("2026-10-26 14:00")
	row := system.CurrencyReferenceRate{Pair: Pair, Rate: 0.85634, RateDate: "2026-10-26", Source: Source, FetchedAt: now.Unix()}
	if err := Validate(row, now); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		edit func(*system.CurrencyReferenceRate)
	}{
		{"旧汇率即使刚同步也不放行", func(r *system.CurrencyReferenceRate) { r.RateDate = "2026-10-23" }},
		{"未来日期", func(r *system.CurrencyReferenceRate) { r.RateDate = "2026-10-27" }},
		{"零值", func(r *system.CurrencyReferenceRate) { r.Rate = 0 }},
		{"错误币对", func(r *system.CurrencyReferenceRate) { r.Pair = "CNY/HKD" }},
		{"人工来源", func(r *system.CurrencyReferenceRate) { r.Source = "manual" }},
		{"未来同步时间", func(r *system.CurrencyReferenceRate) { r.FetchedAt += 61 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			copy := row
			tc.edit(&copy)
			if err := Validate(copy, now); err == nil {
				t.Fatal("异常汇率不应允许兑换")
			}
		})
	}
	weekend := clock("2026-10-25 14:00")
	row.RateDate, row.FetchedAt = "2026-10-23", clock("2026-10-23 10:00").Unix()
	if err := Validate(row, weekend); err != nil {
		t.Fatalf("周末应可用周五公布值：%v", err)
	}
}

func TestFetchDailyRate(t *testing.T) {
	now := clock("2026-10-26 14:00")
	for _, tc := range []struct {
		name, body string
		status     int
		bad        bool
	}{
		{"当日有效", `{"head":{"rep_code":"200"},"data":{"currency":"HKD/CNY"},"records":[{"date":"2026-10-23","values":["0.85696"]},{"date":"2026-10-26","values":["0.85634"]}]}`, 200, false},
		{"未更新", `{"head":{"rep_code":"200"},"data":{"currency":"HKD/CNY"},"records":[{"date":"2026-10-23","values":["0.85696"]}]}`, 200, true},
		{"返回错误", `{}`, 503, true},
		{"网页而非数据", `<html>拒绝访问</html>`, 200, true},
		{"无数据", `{"head":{"rep_code":"200"},"data":{"currency":"HKD/CNY"},"records":[]}`, 200, true},
		{"错误币对", `{"head":{"rep_code":"200"},"data":{"currency":"USD/CNY"},"records":[{"date":"2026-10-26","values":["6.7"]}]}`, 200, true},
		{"错误业务码", `{"head":{"rep_code":"500"},"data":{"currency":"HKD/CNY"},"records":[{"date":"2026-10-26","values":["0.85634"]}]}`, 200, true},
		{"未来日期", `{"head":{"rep_code":"200"},"data":{"currency":"HKD/CNY"},"records":[{"date":"2026-10-27","values":["0.85634"]}]}`, 200, true},
		{"非数值", `{"head":{"rep_code":"200"},"data":{"currency":"HKD/CNY"},"records":[{"date":"2026-10-26","values":["NaN"]}]}`, 200, true},
		{"零汇率", `{"head":{"rep_code":"200"},"data":{"currency":"HKD/CNY"},"records":[{"date":"2026-10-26","values":["0"]}]}`, 200, true},
		{"重复日期", `{"head":{"rep_code":"200"},"data":{"currency":"HKD/CNY"},"records":[{"date":"2026-10-26","values":["0.85634"]},{"date":"2026-10-26","values":["0.9"]}]}`, 200, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("currency") != Pair || r.URL.Query().Get("endDate") != "2026-10-26" {
					t.Error("请求必须指定港币兑人民币及当前日期")
				}
				w.WriteHeader(tc.status)
				fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			row, err := Fetch(context.Background(), server.Client(), server.URL, now)
			if (err != nil) != tc.bad {
				t.Fatalf("row=%+v err=%v", row, err)
			}
			if !tc.bad && (row.Rate != 0.85634 || row.RateDate != "2026-10-26" || row.Source != Source) {
				t.Fatalf("不得颠倒币对或用同步时间冒充公布日期：%+v", row)
			}
		})
	}
}

func TestFetchDailyRateLive(t *testing.T) {
	if os.Getenv("EXCHANGE_RATE_LIVE_TEST") != "1" {
		t.Skip("未启用公开汇率只读验证")
	}
	now := time.Now()
	row, err := Fetch(context.Background(), &http.Client{Timeout: 15 * time.Second}, Endpoint, now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(row.Source, "中国货币网") {
		t.Fatal("来源不符合要求")
	}
	t.Logf("1 HKD = %.8f CNY，公布日期%s，来源%s", row.Rate, row.RateDate, row.Source)
}

func TestSaveDailyRateSQL(t *testing.T) {
	db, err := gorm.Open(postgres.Open("host=127.0.0.1 user=test dbname=test sslmode=disable"),
		&gorm.Config{DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true})
	if err != nil {
		t.Fatal(err)
	}
	now := clock("2026-10-26 14:00")
	row := system.CurrencyReferenceRate{Pair: Pair, Rate: 0.85634, RateDate: "2026-10-26", Source: Source, FetchedAt: now.Unix()}
	sql := db.ToSQL(func(tx *gorm.DB) *gorm.DB {
		tx = tx.Model(&system.CurrencyReferenceRate{})
		if err := Save(tx, row, now); err != nil {
			t.Fatal(err)
		}
		return tx
	})
	for _, required := range []string{`ON CONFLICT ("pair") DO UPDATE`, "currency_reference_rates.rate_date <= EXCLUDED.rate_date",
		"currency_reference_rates.fetched_at <= EXCLUDED.fetched_at"} {
		if !strings.Contains(sql, required) {
			t.Fatalf("持久化必须防止旧日期、晚到旧任务覆盖新数据，缺少%s：%s", required, sql)
		}
	}
}
