package hkquote

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const EastmoneyURL = "https://push2delay.eastmoney.com/api/qt/ulist.np/get"

type Quote struct {
	Code       string
	LastPrice  float64
	ChangeRate float64
	Volume     float64
	Amount     float64
	QuoteAt    int64
}

func FetchEastmoney(ctx context.Context, client *http.Client, endpoint string, codes []string) (map[string]Quote, error) {
	if len(codes) == 0 || len(codes) > 100 {
		return nil, fmt.Errorf("东方财富港股每批须为1至100只")
	}
	secids := make([]string, 0, len(codes))
	requested := make(map[string]bool, len(codes))
	for _, code := range codes {
		if len(code) != 5 || strings.IndexFunc(code, func(r rune) bool { return r < '0' || r > '9' }) >= 0 || requested[code] {
			return nil, fmt.Errorf("东方财富港股代码无效或重复：%s", code)
		}
		requested[code] = true
		secids = append(secids, "116."+code)
	}
	query := url.Values{
		"secids": {strings.Join(secids, ",")},
		"fields": {"f12,f13,f2,f3,f5,f6,f124"},
		"fltt":   {"2"},
		"invt":   {"2"},
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+"?"+query.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://quote.eastmoney.com/")
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("东方财富港股响应状态码 %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 2<<20 {
		return nil, fmt.Errorf("东方财富港股响应超过大小限制")
	}
	return parseEastmoney(body, requested, time.Now())
}

func parseEastmoney(body []byte, requested map[string]bool, now time.Time) (map[string]Quote, error) {
	var payload struct {
		RC   *int `json:"rc"`
		Data *struct {
			Diff []struct {
				Code       string          `json:"f12"`
				Market     int             `json:"f13"`
				Price      json.RawMessage `json:"f2"`
				ChangeRate json.RawMessage `json:"f3"`
				Volume     json.RawMessage `json:"f5"`
				Amount     json.RawMessage `json:"f6"`
				QuoteAt    json.RawMessage `json:"f124"`
			} `json:"diff"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("东方财富港股响应格式无效：%w", err)
	}
	if payload.RC == nil || *payload.RC != 0 || payload.Data == nil || payload.Data.Diff == nil {
		return nil, fmt.Errorf("东方财富港股未返回有效数据")
	}
	quotes := make(map[string]Quote)
	seen := make(map[string]bool)
	for _, item := range payload.Data.Diff {
		if !requested[item.Code] || item.Market != 116 || seen[item.Code] {
			return nil, fmt.Errorf("东方财富港股代码或市场不匹配：%s", item.Code)
		}
		seen[item.Code] = true
		rawFields := []json.RawMessage{item.Price, item.ChangeRate, item.Volume, item.Amount, item.QuoteAt}
		values := make([]float64, len(rawFields))
		missing := false
		for index, raw := range rawFields {
			text := strings.Trim(string(raw), `"`)
			if text == "" || text == "-" || text == "--" || text == "null" {
				missing = true
				continue
			}
			value, err := strconv.ParseFloat(text, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				return nil, fmt.Errorf("东方财富港股字段无效：%s", item.Code)
			}
			values[index] = value
		}
		if missing {
			continue
		}
		if values[0] <= 0 || values[0] > 1_000_000 || values[2] < 0 || values[3] < 0 ||
			values[4] <= 0 || values[4] != math.Trunc(values[4]) || values[4] > float64(now.Unix()+60) {
			continue
		}
		quotes[item.Code] = Quote{Code: item.Code, LastPrice: values[0], ChangeRate: values[1], Volume: values[2], Amount: values[3], QuoteAt: int64(values[4])}
	}
	return quotes, nil
}
