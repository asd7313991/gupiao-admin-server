package stock

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"api-server/db/pgdb/system"
)

func fetchTencentBatch(ctx context.Context, client *http.Client, symbols []string) (map[string]tencentQuote, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://qt.gtimg.cn/q="+url.QueryEscape(strings.Join(symbols, ",")), nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	request.Header.Set("Referer", "https://gu.qq.com/")
	result, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("腾讯行情响应状态异常：%d", result.StatusCode)
	}
	body, err := io.ReadAll(transform.NewReader(result.Body, simplifiedchinese.GBK.NewDecoder()))
	if err != nil {
		return nil, err
	}
	return parseTencentQuotes(string(body)), nil
}

func fetchTencentSnapshot(client *http.Client, existing []system.StockSecurity) ([]system.StockSecurity, error) {
	if len(existing) == 0 {
		return nil, fmt.Errorf("没有已收录的A股证券，无法使用备用行情")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	items := append([]system.StockSecurity(nil), existing...)
	valid := 0
	for start := 0; start < len(items); start += 400 {
		batches := min(4, (len(items)-start+99)/100)
		quotes := make([]map[string]tencentQuote, batches)
		errors := make([]error, batches)
		var workers sync.WaitGroup
		for batch := 0; batch < batches; batch++ {
			workers.Add(1)
			go func(batch int) {
				defer workers.Done()
				symbols := make([]string, 0, 100)
				for _, item := range items[start+batch*100 : min(start+(batch+1)*100, len(items))] {
					symbols = append(symbols, tencentSymbol(item))
				}
				quotes[batch], errors[batch] = fetchTencentBatch(ctx, client, symbols)
			}(batch)
		}
		workers.Wait()
		for batch, err := range errors {
			if err != nil {
				return nil, err
			}
			for index := start + batch*100; index < min(start+(batch+1)*100, len(items)); index++ {
				item := &items[index]
				quote, exists := quotes[batch][tencentSymbol(*item)]
				item.LastPrice, item.ChangeRate, item.Volume, item.Amount, item.Turnover, item.QuoteAt = 0, 0, 0, 0, 0, 0
				item.Source, item.UpdatedAt = "tencent/unavailable", time.Now()
				if !exists || !quote.MarketData {
					continue
				}
				item.LastPrice, item.ChangeRate, item.Volume = quote.Price, quote.ChangeRate, quote.Volume
				item.Amount, item.Turnover, item.QuoteAt, item.Source = quote.Amount, quote.Turnover, quote.QuoteAt, "tencent"
				valid++
			}
		}
	}
	if valid == 0 {
		return nil, fmt.Errorf("腾讯全市场备用行情没有有效报价，已取消写入")
	}
	return items, nil
}
