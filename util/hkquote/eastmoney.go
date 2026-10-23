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
	markets := make(map[string]int, len(codes))
	for _, code := range codes {
		markets[code] = 116
	}
	return fetchEastmoneyMarkets(ctx, client, endpoint, secids, requested, markets)
}

func FetchEastmoneySymbols(ctx context.Context, client *http.Client, endpoint string, symbols []string) (map[string]Quote, error) {
	if len(symbols) == 0 || len(symbols) > 100 {
		return nil, fmt.Errorf("东方财富每批须为1至100只")
	}
	secids := make([]string, 0, len(symbols))
	requested, markets, byCode := make(map[string]bool), make(map[string]int), make(map[string]string)
	for _, symbol := range symbols {
		parts := strings.Split(symbol, ".")
		if len(parts) != 2 {
			return nil, fmt.Errorf("东方财富证券代码无效：%s", symbol)
		}
		code, size, market := parts[0], 6, 0
		switch parts[1] {
		case "SH":
			market = 1
		case "SZ", "BJ":
		case "HK":
			size, market = 5, 116
		default:
			return nil, fmt.Errorf("东方财富证券市场无效：%s", symbol)
		}
		if len(code) != size || strings.Trim(code, "0123456789") != "" || requested[code] {
			return nil, fmt.Errorf("东方财富证券代码无效或重复：%s", symbol)
		}
		requested[code], markets[code], byCode[code] = true, market, symbol
		secids = append(secids, fmt.Sprintf("%d.%s", market, code))
	}
	values, err := fetchEastmoneyMarkets(ctx, client, endpoint, secids, requested, markets)
	if err != nil {
		return nil, err
	}
	quotes := make(map[string]Quote, len(values))
	for code, quote := range values {
		quotes[byCode[code]] = quote
	}
	return quotes, nil
}

func fetchEastmoneyMarkets(ctx context.Context, client *http.Client, endpoint string, secids []string, requested map[string]bool, markets map[string]int) (map[string]Quote, error) {
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
		return nil, fmt.Errorf("东方财富行情响应状态码 %d", res.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(res.Body, (2<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(body) > 2<<20 {
		return nil, fmt.Errorf("东方财富行情响应超过大小限制")
	}
	return parseEastmoneyMarkets(body, requested, markets, time.Now())
}

func parseEastmoney(body []byte, requested map[string]bool, now time.Time) (map[string]Quote, error) {
	markets := make(map[string]int, len(requested))
	for code := range requested {
		markets[code] = 116
	}
	return parseEastmoneyMarkets(body, requested, markets, now)
}

func parseEastmoneyMarkets(body []byte, requested map[string]bool, markets map[string]int, now time.Time) (map[string]Quote, error) {
	var payload struct {
		RC   *int `json:"rc"`
		Data *struct {
			Diff []struct {
				Code       string          `json:"f12"`
				Market     *int            `json:"f13"`
				Price      json.RawMessage `json:"f2"`
				ChangeRate json.RawMessage `json:"f3"`
				Volume     json.RawMessage `json:"f5"`
				Amount     json.RawMessage `json:"f6"`
				QuoteAt    json.RawMessage `json:"f124"`
			} `json:"diff"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return nil, fmt.Errorf("东方财富行情响应格式无效：%w", err)
	}
	if payload.RC == nil || *payload.RC != 0 || payload.Data == nil || payload.Data.Diff == nil {
		return nil, fmt.Errorf("东方财富行情未返回有效数据")
	}
	quotes := make(map[string]Quote)
	seen := make(map[string]bool)
	for _, item := range payload.Data.Diff {
		if !requested[item.Code] || item.Market == nil || *item.Market != markets[item.Code] || seen[item.Code] {
			return nil, fmt.Errorf("东方财富行情代码或市场不匹配：%s", item.Code)
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
				return nil, fmt.Errorf("东方财富行情字段无效：%s", item.Code)
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
