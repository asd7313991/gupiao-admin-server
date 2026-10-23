package positionquote

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"api-server/util/hkquote"
)

func FetchLatest(ctx context.Context, client *http.Client, tencentURL, eastmoneyURL string, symbols []string, now time.Time) (map[string]Quote, error) {
	aSymbols, hkSymbols := make([]string, 0), make([]string, 0)
	seen := make(map[string]bool)
	for _, symbol := range symbols {
		if Symbol(symbol) == "" {
			return nil, fmt.Errorf("持仓证券代码无效：%s", symbol)
		}
		if seen[symbol] {
			continue
		}
		seen[symbol] = true
		if strings.HasSuffix(symbol, ".HK") {
			hkSymbols = append(hkSymbols, symbol)
		} else {
			aSymbols = append(aSymbols, symbol)
		}
	}
	quotes, err := Fetch(ctx, client, tencentURL, aSymbols, now)
	if err != nil {
		return nil, err
	}
	for start := 0; start < len(hkSymbols); start += 100 {
		batch := hkSymbols[start:min(start+100, len(hkSymbols))]
		codes, tencentCodes := make([]string, 0, len(batch)), make([]string, 0, len(batch))
		requested := make(map[string]string)
		for _, symbol := range batch {
			codes, tencentCodes = append(codes, symbol[:5]), append(tencentCodes, Symbol(symbol))
			requested[Symbol(symbol)] = symbol
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
			eastmoney, eastmoneyErr = hkquote.FetchEastmoney(ctx, client, eastmoneyURL, codes)
		}()
		workers.Wait()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		if tencentErr != nil {
			zap.L().Warn("腾讯港股持仓报价部分不可用，核对备用来源", zap.Error(tencentErr))
		}
		if eastmoneyErr != nil {
			zap.L().Warn("东方财富港股持仓报价不可用，核对备用来源", zap.Error(eastmoneyErr))
		}
		for _, symbol := range batch {
			best := tencent[symbol]
			if candidate, ok := eastmoney[symbol[:5]]; ok && ValidateTime(candidate.QuoteAt, now, true) == nil && candidate.QuoteAt >= best.At {
				best = Quote{Price: candidate.LastPrice, At: candidate.QuoteAt, Source: "eastmoney"}
			}
			if best.Price <= 0 || best.At <= 0 {
				return nil, fmt.Errorf("%s 没有通过时效检查的港股持仓报价，请稍后刷新", symbol)
			}
			quotes[symbol] = best
		}
	}
	return quotes, nil
}
