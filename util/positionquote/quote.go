package positionquote

import (
	"context"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

type Quote struct {
	Price  float64
	At     int64
	Source string
}

func Symbol(symbol string) string {
	parts := strings.Split(symbol, ".")
	if len(parts) != 2 {
		return ""
	}
	size := 6
	if parts[1] == "HK" {
		size = 5
	}
	if len(parts[0]) != size || strings.IndexFunc(parts[0], func(r rune) bool { return r < '0' || r > '9' }) >= 0 {
		return ""
	}
	switch parts[1] {
	case "SH", "SZ", "BJ", "HK":
		return strings.ToLower(parts[1]) + parts[0]
	}
	return ""
}

func ValidateTime(at int64, now time.Time, hk bool) error {
	if at <= 0 || at > now.Unix()+60 {
		return fmt.Errorf("报价时间无效或超前")
	}
	location := time.FixedZone("UTC+8", 8*3600)
	current, quoted := now.In(location), time.Unix(at, 0).In(location)
	minute := current.Hour()*60 + current.Minute()
	closeMinute, morningEnd := 15*60, 11*60+30
	if hk {
		closeMinute, morningEnd = 16*60, 12*60
	}
	weekday := current.Weekday() != time.Saturday && current.Weekday() != time.Sunday
	if weekday && minute >= 9*60+30 && minute < closeMinute {
		if minute >= morningEnd && minute < 13*60 {
			if quoted.Format("2006-01-02") == current.Format("2006-01-02") && quoted.Hour()*60+quoted.Minute() >= morningEnd-30 {
				return nil
			}
		} else if now.Unix()-at <= 30*60 {
			return nil
		}
		return fmt.Errorf("交易时段持仓报价已过期，请稍后刷新")
	}
	expected := current
	if !weekday || minute < 9*60+30 {
		expected = expected.AddDate(0, 0, -1)
		for expected.Weekday() == time.Saturday || expected.Weekday() == time.Sunday {
			expected = expected.AddDate(0, 0, -1)
		}
	}
	if quoted.Format("2006-01-02") != expected.Format("2006-01-02") || quoted.Hour()*60+quoted.Minute() < closeMinute-30 {
		return fmt.Errorf("缺少最近工作日的有效收市阶段报价")
	}
	return nil
}

func Parse(body string, requested map[string]string, now time.Time) (map[string]Quote, error) {
	quotes, err := parseAvailable(body, requested, now)
	if err != nil {
		return nil, err
	}
	return quotes, nil
}

func parseAvailable(body string, requested map[string]string, now time.Time) (map[string]Quote, error) {
	quotes := make(map[string]Quote)
	seen := make(map[string]bool)
	var firstErr error
	for _, line := range strings.Split(body, ";") {
		line = strings.TrimSpace(line)
		separator := strings.Index(line, "=")
		if separator < 0 {
			continue
		}
		symbol, ok := requested[strings.TrimPrefix(line[:separator], "v_")]
		if !ok {
			continue
		}
		if seen[symbol] {
			delete(quotes, symbol)
			firstErr = fmt.Errorf("%s 返回重复报价", symbol)
			continue
		}
		seen[symbol] = true
		fields := strings.Split(strings.Trim(line[separator+1:], `"`), "~")
		quote, err := parseOne(symbol, fields, now)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		quotes[symbol] = quote
	}
	for _, symbol := range requested {
		if _, ok := quotes[symbol]; !ok {
			if firstErr == nil {
				firstErr = fmt.Errorf("%s 缺少有效持仓报价", symbol)
			}
		}
	}
	return quotes, firstErr
}

func parseOne(symbol string, fields []string, now time.Time) (Quote, error) {
	if len(fields) <= 30 || fields[2] != strings.Split(symbol, ".")[0] {
		return Quote{}, fmt.Errorf("%s 报价代码不匹配或字段不完整", symbol)
	}
	price, err := strconv.ParseFloat(fields[3], 64)
	if err != nil || math.IsNaN(price) || math.IsInf(price, 0) || price <= 0 || price > 1_000_000 {
		return Quote{}, fmt.Errorf("%s 持仓价格无效", symbol)
	}
	hk := strings.HasSuffix(symbol, ".HK")
	layout := "20060102150405"
	if hk {
		layout = "2006/01/02 15:04:05"
	}
	at, err := time.ParseInLocation(layout, fields[30], time.FixedZone("UTC+8", 8*3600))
	if err != nil {
		return Quote{}, fmt.Errorf("%s 持仓报价时间无效", symbol)
	}
	if err := ValidateTime(at.Unix(), now, hk); err != nil {
		return Quote{}, fmt.Errorf("%s：%w", symbol, err)
	}
	return Quote{Price: price, At: at.Unix(), Source: "tencent"}, nil
}

func Fetch(ctx context.Context, client *http.Client, endpoint string, symbols []string, now time.Time) (map[string]Quote, error) {
	requested := make(map[string]string)
	codes := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		code := Symbol(symbol)
		if code == "" {
			return nil, fmt.Errorf("持仓证券代码无效：%s", symbol)
		}
		if _, exists := requested[code]; !exists {
			requested[code] = symbol
			codes = append(codes, code)
		}
	}
	quotes := make(map[string]Quote)
	for start := 0; start < len(codes); start += 100 {
		end := min(start+100, len(codes))
		batch := make(map[string]string)
		for _, code := range codes[start:end] {
			batch[code] = requested[code]
		}
		body, err := fetchTencentBody(ctx, client, endpoint, codes[start:end])
		if err != nil {
			return nil, err
		}
		values, err := Parse(string(body), batch, now)
		if err != nil {
			return nil, err
		}
		for symbol, quote := range values {
			quotes[symbol] = quote
		}
	}
	return quotes, nil
}

func fetchTencentBody(ctx context.Context, client *http.Client, endpoint string, codes []string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+url.QueryEscape(strings.Join(codes, ",")), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://gu.qq.com/")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, err := io.ReadAll(io.LimitReader(transform.NewReader(res.Body, simplifiedchinese.GBK.NewDecoder()), (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if res.StatusCode != http.StatusOK || len(body) > 2<<20 {
		return nil, fmt.Errorf("持仓行情响应异常：%d", res.StatusCode)
	}
	return body, nil
}
