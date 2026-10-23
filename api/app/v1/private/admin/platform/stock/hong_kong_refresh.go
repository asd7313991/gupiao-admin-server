package stock

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"api-server/util/hkquote"
)

var ErrHongKongSyncRunning = errors.New("港股采集正在执行")

func RefreshHongKongQuotes(maxRows int) (count int, err error) {
	hkSync.Lock()
	if hkSync.status.State == "running" {
		hkSync.Unlock()
		return 0, ErrHongKongSyncRunning
	}
	status := hongKongSyncStatus{State: "running", Stage: "正在自动更新已收录港股报价", StartedAt: time.Now().Unix()}
	hkSync.status = status
	hkSync.Unlock()
	defer func() {
		status.FinishedAt = time.Now().Unix()
		if err != nil {
			status.State, status.Stage, status.Error = "failed", "港股报价更新失败，保留原报价时间", err.Error()
		} else {
			status.State, status.Stage, status.Synced = "success", "港股报价自动更新完成", count
		}
		hkSync.Lock()
		hkSync.status = status
		hkSync.Unlock()
	}()
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	if maxRows <= 0 || maxRows > 10000 {
		maxRows = 10000
	}
	var existing []system.StockSecurity
	err = pgdb.GetClient().WithContext(ctx).Where("market = ? AND symbol LIKE ?", "港股", "%.HK").Order("code").Limit(maxRows).Find(&existing).Error
	if err != nil {
		return 0, err
	}
	items, err := refreshHongKongSnapshot(ctx, &http.Client{Timeout: 8 * time.Second}, hkQuoteURL, hkquote.EastmoneyURL, existing)
	if err != nil {
		return 0, err
	}
	status.Fetched, status.Quoted = len(items), len(items)
	for _, item := range items {
		switch item.Source {
		case "eastmoney":
			status.Eastmoney++
		case "sina/tencent":
			status.Tencent++
		default:
			status.Unavailable++
		}
	}
	if status.Unavailable > 0 {
		zap.L().Warn("部分港股没有新报价，保留其原报价时间", zap.Int("count", status.Unavailable))
	}
	if err = saveHongKongSecurities(ctx, pgdb.GetClient(), items); err != nil {
		return 0, err
	}
	return len(items), nil
}

func refreshHongKongSnapshot(ctx context.Context, client *http.Client, quoteURL, eastmoneyURL string, existing []system.StockSecurity) ([]system.StockSecurity, error) {
	if len(existing) == 0 {
		return nil, fmt.Errorf("尚无已收录港股，请先在后台同步港股列表")
	}
	items := append([]system.StockSecurity(nil), existing...)
	for index := range items {
		if !hkCodePattern.MatchString(items[index].Code) {
			return nil, fmt.Errorf("港股代码无效：%s", items[index].Code)
		}
		items[index].LastPrice, items[index].ChangeRate, items[index].Volume, items[index].Amount, items[index].QuoteAt = 0, 0, 0, 0, 0
		items[index].Source, items[index].UpdatedAt = "sina/unavailable", time.Now()
	}
	for start := 0; start < len(items); start += 400 {
		var workers sync.WaitGroup
		errors := make([]error, min(4, (len(items)-start+99)/100))
		for batch := range errors {
			workers.Add(1)
			go func(batch int) {
				defer workers.Done()
				errors[batch] = updateHongKongBatch(ctx, client, quoteURL, eastmoneyURL, items[start+batch*100:min(start+(batch+1)*100, len(items))])
			}(batch)
		}
		workers.Wait()
		for _, err := range errors {
			if err != nil {
				return nil, err
			}
		}
	}
	for _, item := range items {
		if item.LastPrice > 0 && item.QuoteAt > 0 {
			return items, nil
		}
	}
	return nil, fmt.Errorf("港股主备来源均没有有效报价，未发布新快照")
}

func updateHongKongBatch(ctx context.Context, client *http.Client, quoteURL, eastmoneyURL string, items []system.StockSecurity) error {
	codes, symbols := make([]string, 0, len(items)), make([]string, 0, len(items))
	for _, item := range items {
		codes, symbols = append(codes, item.Code), append(symbols, "hk"+item.Code)
	}
	var eastmoney map[string]hkquote.Quote
	var tencent map[string]system.StockSecurity
	var eastmoneyErr, tencentErr error
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		eastmoney, eastmoneyErr = hkquote.FetchEastmoney(ctx, client, eastmoneyURL, codes)
	}()
	go func() {
		defer workers.Done()
		var body []byte
		body, tencentErr = fetchHongKongBody(ctx, client, quoteURL+"?q="+url.QueryEscape(strings.Join(symbols, ",")), "https://gu.qq.com/", true)
		if tencentErr == nil {
			tencent, tencentErr = parseHongKongTencentQuotes(string(body))
		}
	}()
	workers.Wait()
	if eastmoneyErr != nil {
		zap.L().Warn("东方财富港股报价失败，尝试备用来源", zap.Error(eastmoneyErr))
	}
	if tencentErr != nil {
		zap.L().Warn("腾讯港股报价失败，尝试其他来源", zap.Error(tencentErr))
	}
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if eastmoneyErr != nil && tencentErr != nil {
		hasSina := false
		for _, item := range items {
			hasSina = hasSina || (item.LastPrice > 0 && item.QuoteAt > 0)
		}
		if !hasSina {
			return fmt.Errorf("港股主备报价均失败：东方财富：%v；腾讯：%v", eastmoneyErr, tencentErr)
		}
	}
	for index := range items {
		security := &items[index]
		if quote, ok := tencent[security.Code]; ok && quote.QuoteAt <= time.Now().Unix()+60 && quote.QuoteAt >= security.QuoteAt {
			security.LastPrice, security.ChangeRate = quote.LastPrice, quote.ChangeRate
			security.Volume, security.Amount, security.QuoteAt = quote.Volume, quote.Amount, quote.QuoteAt
			security.Source = "sina/tencent"
		}
		if quote, ok := eastmoney[security.Code]; ok && quote.QuoteAt >= security.QuoteAt {
			security.LastPrice, security.ChangeRate = quote.LastPrice, quote.ChangeRate
			security.Volume, security.Amount, security.QuoteAt = quote.Volume, quote.Amount, quote.QuoteAt
			security.Source = "eastmoney"
		}
	}
	return nil
}
