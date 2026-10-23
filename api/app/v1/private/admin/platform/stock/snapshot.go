package stock

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"api-server/db/pgdb/system"
)

var eastmoneySyncLock sync.Mutex

func fetchEastmoneySnapshot(client *http.Client, maxSyncRows int) ([]system.StockSecurity, error) {
	if maxSyncRows <= 0 || maxSyncRows > 10000 {
		maxSyncRows = 10000
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	first, err := fetchEastmoneyPage(ctx, client, 1)
	if err != nil {
		return nil, err
	}
	limit := min(first.Data.Total, maxSyncRows)
	pages := make([]eastmoneyResponse, (limit+99)/100)
	pages[0] = first
	// 以代码稳定分页，避免按实时涨跌幅分页时股票跨页导致遗漏或重复。
	for start := 1; start < len(pages); start += 4 {
		var workers sync.WaitGroup
		errors := make([]error, min(4, len(pages)-start))
		for offset := range errors {
			workers.Add(1)
			go func(offset int) {
				defer workers.Done()
				pages[start+offset], errors[offset] = fetchEastmoneyPage(ctx, client, start+offset+1)
			}(offset)
		}
		workers.Wait()
		for _, err := range errors {
			if err != nil {
				return nil, err
			}
		}
	}
	securities := make([]system.StockSecurity, 0, limit)
	seen := make(map[string]bool, limit)
	for page, payload := range pages {
		expected := min(100, limit-page*100)
		if len(payload.Data.Diff) < expected {
			return nil, fmt.Errorf("第 %d 页行情不完整：需要 %d 条，收到 %d 条", page+1, expected, len(payload.Data.Diff))
		}
		for _, item := range payload.Data.Diff[:expected] {
			if len(item.Code) != 6 || strings.Trim(item.Code, "0123456789") != "" || seen[item.Code] || (item.Market != 0 && item.Market != 1) {
				return nil, fmt.Errorf("第 %d 页证券代码无效或重复：%s", page+1, item.Code)
			}
			seen[item.Code] = true
			suffix, exchange := "SZ", "深交所"
			if item.Market == 1 {
				suffix, exchange = "SH", "上交所"
			}
			board := classifyBoard(item.Code, item.Market)
			if board == "北交所" {
				suffix, exchange = "BJ", "北交所"
			}
			securities = append(securities, system.StockSecurity{
				Code: item.Code, Symbol: item.Code + "." + suffix, Market: "A股",
				Name: item.Name, Exchange: exchange, Board: board, ListDate: string(item.ListDate),
				LastPrice: float64(item.Price), ChangeRate: float64(item.ChangeRate),
				Volume: float64(item.Volume), Amount: float64(item.Amount), Turnover: float64(item.Turnover),
				Status: system.StatusEnabled, Source: "eastmoney", QuoteAt: item.QuoteAt,
			})
		}
	}
	if err := fillTencentPrices(client, securities); err != nil {
		return nil, err
	}
	validPrices := 0
	for index := range securities {
		if securities[index].LastPrice > 0 {
			validPrices++
		} else {
			securities[index].Source = "eastmoney/unavailable"
			securities[index].QuoteAt = 0
		}
	}
	if validPrices == 0 {
		return nil, fmt.Errorf("全市场行情没有有效价格，已取消写入")
	}
	return securities, nil
}

func fetchEastmoneyPage(ctx context.Context, client *http.Client, page int) (eastmoneyResponse, error) {
	q := url.Values{
		"pn": {fmt.Sprint(page)}, "pz": {"100"}, "po": {"1"}, "np": {"1"},
		"ut": {"bd1d9ddb04089700cf9c27f6f7426281"}, "fltt": {"2"}, "invt": {"2"}, "fid": {"f12"},
		"fs":     {"m:0+t:6,m:0+t:80,m:1+t:2,m:1+t:23,m:0+t:81+s:2048"},
		"fields": {"f12,f13,f14,f26,f2,f3,f5,f6,f8,f124"},
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://push2delay.eastmoney.com/api/qt/clist/get?"+q.Encode(), nil)
	if err != nil {
		return eastmoneyResponse{}, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	request.Header.Set("Referer", "https://quote.eastmoney.com/")
	resp, err := client.Do(request)
	if err != nil {
		return eastmoneyResponse{}, err
	}
	defer resp.Body.Close()
	var payload eastmoneyResponse
	if resp.StatusCode != http.StatusOK {
		return payload, fmt.Errorf("东方财富第 %d 页响应状态异常：%d", page, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return payload, err
	}
	if payload.RC != 0 || payload.Data.Total <= 0 || len(payload.Data.Diff) == 0 {
		return payload, fmt.Errorf("东方财富第 %d 页没有行情数据", page)
	}
	return payload, nil
}
