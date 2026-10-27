package exchangerate

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"api-server/db/pgdb/system"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	Pair     = "HKD/CNY"
	Source   = "中国货币网（中国外汇交易中心）"
	Endpoint = "https://www.chinamoney.com.cn/ags/ms/cm-u-bk-ccpr/CcprHisNew"
)

var Location = time.FixedZone("UTC+8", 8*60*60)

func Valid(rate float64) bool {
	return !math.IsNaN(rate) && !math.IsInf(rate, 0) && rate >= 0.00000001 && rate <= 100 &&
		math.Abs(rate*1e8-math.Round(rate*1e8)) < 0.000001
}

// 2026年假期依据国办发明电〔2025〕7号；周末调休上班不视为汇率公布日。
var holidays2026 = [][2]string{
	{"2026-01-01", "2026-01-03"}, {"2026-02-15", "2026-02-23"},
	{"2026-04-04", "2026-04-06"}, {"2026-05-01", "2026-05-05"},
	{"2026-06-19", "2026-06-21"}, {"2026-09-25", "2026-09-27"},
	{"2026-10-01", "2026-10-07"},
}

func publicationDay(day time.Time) bool {
	if day.Weekday() == time.Saturday || day.Weekday() == time.Sunday {
		return false
	}
	date := day.Format(time.DateOnly)
	for _, period := range holidays2026 {
		if date >= period[0] && date <= period[1] {
			return false
		}
	}
	return true
}

func ExpectedDate(now time.Time) string {
	now = now.In(Location)
	day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, Location)
	if now.Hour()*60+now.Minute() < 9*60+15 {
		day = day.AddDate(0, 0, -1)
	}
	for !publicationDay(day) {
		day = day.AddDate(0, 0, -1)
	}
	return day.Format(time.DateOnly)
}

func Validate(row system.CurrencyReferenceRate, now time.Time) error {
	if row.Pair != Pair || row.Source != Source || !Valid(row.Rate) || row.FetchedAt <= 0 || row.FetchedAt > now.Unix()+60 {
		return fmt.Errorf("港币每日汇率无效，暂不能兑换")
	}
	if _, err := time.Parse(time.DateOnly, row.RateDate); err != nil {
		return fmt.Errorf("港币汇率公布日期无效，暂不能兑换")
	}
	expected := ExpectedDate(now)
	if row.RateDate != expected {
		return fmt.Errorf("港币汇率尚未更新至应公布日期%s（现有%s），暂不能兑换，请稍后刷新", expected, row.RateDate)
	}
	return nil
}

func Fetch(ctx context.Context, client *http.Client, endpoint string, now time.Time) (system.CurrencyReferenceRate, error) {
	var row system.CurrencyReferenceRate
	address, err := url.Parse(endpoint)
	if err != nil {
		return row, err
	}
	query := address.Query()
	today := now.In(Location)
	query.Set("startDate", today.AddDate(0, 0, -40).Format(time.DateOnly))
	query.Set("endDate", today.Format(time.DateOnly))
	query.Set("currency", Pair)
	query.Set("pageNum", "1")
	query.Set("pageSize", "40")
	address.RawQuery = query.Encode()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, address.String(), nil)
	if err != nil {
		return row, err
	}
	response, err := client.Do(request)
	if err != nil {
		return row, fmt.Errorf("获取每日港币汇率失败：%w", err)
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return row, fmt.Errorf("每日汇率来源返回HTTP状态%d", response.StatusCode)
	}
	var payload struct {
		Head struct {
			Code string `json:"rep_code"`
		} `json:"head"`
		Data struct {
			Currency string `json:"currency"`
		} `json:"data"`
		Records []struct {
			Date   string   `json:"date"`
			Values []string `json:"values"`
		} `json:"records"`
	}
	if err := json.NewDecoder(io.LimitReader(response.Body, 128*1024)).Decode(&payload); err != nil {
		return row, fmt.Errorf("解析每日汇率失败：%w", err)
	}
	if payload.Head.Code != "200" || payload.Data.Currency != Pair || len(payload.Records) == 0 {
		return row, fmt.Errorf("每日港币汇率来源未返回有效数据")
	}
	seen := make(map[string]bool)
	for _, item := range payload.Records {
		date, err := time.ParseInLocation(time.DateOnly, item.Date, Location)
		if err != nil || item.Date > today.Format(time.DateOnly) || date.Format(time.DateOnly) < query.Get("startDate") ||
			len(item.Values) != 1 || seen[item.Date] {
			return row, fmt.Errorf("每日汇率包含无效、重复或未来日期")
		}
		seen[item.Date] = true
		rate, err := strconv.ParseFloat(item.Values[0], 64)
		if err != nil || !Valid(rate) {
			return row, fmt.Errorf("每日港币汇率数值无效")
		}
		if item.Date > row.RateDate {
			row = system.CurrencyReferenceRate{Pair: Pair, Rate: rate, RateDate: item.Date, Source: Source, FetchedAt: now.Unix()}
		}
	}
	if err := Validate(row, now); err != nil {
		return row, err
	}
	return row, nil
}

func Save(db *gorm.DB, row system.CurrencyReferenceRate, now time.Time) error {
	if err := Validate(row, now); err != nil {
		return err
	}
	return db.Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "pair"}},
		DoUpdates: clause.AssignmentColumns([]string{"rate", "rate_date", "source", "fetched_at"}),
		Where: clause.Where{Exprs: []clause.Expression{clause.Expr{
			SQL: "currency_reference_rates.rate_date <= EXCLUDED.rate_date AND currency_reference_rates.fetched_at <= EXCLUDED.fetched_at",
		}}},
	}).Create(&row).Error
}

func Load(db *gorm.DB, now time.Time) (system.CurrencyReferenceRate, error) {
	var row system.CurrencyReferenceRate
	if err := db.Where("pair = ?", Pair).First(&row).Error; err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			return row, fmt.Errorf("每日港币汇率尚未同步，暂不能兑换，请稍后刷新")
		}
		return row, fmt.Errorf("读取每日港币汇率失败：%w", err)
	}
	return row, Validate(row, now)
}
