package positionquote

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"api-server/util/hkquote"
)

func FetchLatest(ctx context.Context, client *http.Client, tencentURL, eastmoneyURL string, symbols []string, now time.Time) (map[string]Quote, error) {
	quotes, err := FetchAvailable(ctx, client, tencentURL, eastmoneyURL, symbols, now)
	if err != nil {
		return nil, err
	}
	return quotes, nil
}

func FetchAvailable(ctx context.Context, client *http.Client, tencentURL, eastmoneyURL string, symbols []string, now time.Time) (map[string]Quote, error) {
	requestSymbols := make([]string, 0, len(symbols))
	seen := make(map[string]bool)
	var missing []error
	for _, symbol := range symbols {
		if seen[symbol] {
			continue
		}
		seen[symbol] = true
		if Symbol(symbol) == "" {
			missing = append(missing, fmt.Errorf("持仓证券代码无效：%s", symbol))
			continue
		}
		requestSymbols = append(requestSymbols, symbol)
	}
	quotes := make(map[string]Quote, len(requestSymbols))
	for start := 0; start < len(requestSymbols); start += 100 {
		batch := requestSymbols[start:min(start+100, len(requestSymbols))]
		tencentCodes := make([]string, 0, len(batch))
		requested := make(map[string]string)
		for _, symbol := range batch {
			for _, code := range TencentCodes(symbol) {
				tencentCodes = append(tencentCodes, code)
				requested[code] = symbol
			}
		}
		var tencent map[string]Quote
		var eastmoney map[string]hkquote.Quote
		var tencentErr, eastmoneyErr error
		var workers sync.WaitGroup
		workers.Add(2)
		go func() {
			defer workers.Done()
			var body []byte
			body, tencentErr = fetchTencentBody(ctx, client, tencentURL, tencentCodes)
			if tencentErr == nil {
				tencent, tencentErr = parseAvailable(string(body), requested, now)
			}
		}()
		go func() {
			defer workers.Done()
			eastmoney, eastmoneyErr = hkquote.FetchEastmoneySymbols(ctx, client, eastmoneyURL, batch)
		}()
		workers.Wait()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if tencentErr != nil {
			zap.L().Warn("腾讯持仓报价部分不可用，核对备用来源", zap.Error(tencentErr))
		}
		if eastmoneyErr != nil {
			zap.L().Warn("东方财富持仓报价不可用，核对备用来源", zap.Error(eastmoneyErr))
		}
		for _, symbol := range batch {
			best := tencent[symbol]
			if candidate, ok := eastmoney[symbol]; ok && ValidateTime(candidate.QuoteAt, now, strings.HasSuffix(symbol, ".HK")) == nil && candidate.QuoteAt >= best.At {
				best = Quote{Price: candidate.LastPrice, At: candidate.QuoteAt, Source: "eastmoney"}
			}
			if best.Price <= 0 || best.At <= 0 {
				missing = append(missing, fmt.Errorf("%s 没有通过时效检查的持仓报价，请稍后刷新", symbol))
				continue
			}
			quotes[symbol] = best
		}
	}
	return quotes, errors.Join(missing...)
}
