package mobile

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	stock "api-server/api/app/v1/private/admin/platform/stock"
	"api-server/db/pgdb/system"
	"api-server/util/hkquote"
	"api-server/util/positionquote"
)

func parseAStockDetailSnapshot(body string, item system.StockSecurity, now time.Time) (securityDetailView, error) {
	code := positionquote.Symbol(item.Symbol)
	if code == "" || isHongKongSecurity(item) {
		return securityDetailView{}, fmt.Errorf("A股证券代码无效")
	}
	view, _, err := parseDetailCandidate(body, item, code, now)
	return view, err
}

func parseHongKongDetailSnapshot(body string, item system.StockSecurity, now time.Time) (securityDetailView, []string, error) {
	if !isHongKongSecurity(item) || positionquote.Symbol(item.Symbol) == "" {
		return securityDetailView{}, nil, fmt.Errorf("港股证券代码无效")
	}
	var best securityDetailView
	var fields []string
	var failures []error
	for _, alias := range positionquote.TencentCodes(item.Symbol) {
		view, candidate, err := parseDetailCandidate(body, item, alias, now)
		if err != nil {
			failures = append(failures, fmt.Errorf("%s：%w", alias, err))
			continue
		}
		if view.QuoteAt > best.QuoteAt {
			best, fields = view, candidate
		}
	}
	if fields == nil {
		return securityDetailView{}, nil, errors.Join(failures...)
	}
	return best, fields, nil
}

func parseDetailCandidate(body string, item system.StockSecurity, code string, now time.Time) (securityDetailView, []string, error) {
	quotes, err := positionquote.Parse(body, map[string]string{code: item.Symbol}, now)
	if err != nil {
		return securityDetailView{}, nil, err
	}
	quote := quotes[item.Symbol]
	var fields []string
	for _, line := range strings.Split(body, ";") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok && key == "v_"+code {
			fields = strings.Split(strings.Trim(value, "\"\r\n "), "~")
			break
		}
	}
	if len(fields) <= 38 {
		return securityDetailView{}, nil, fmt.Errorf("完整报价字段缺失")
	}
	values := make(map[int]float64)
	for _, index := range []int{4, 5, 6, 31, 32, 33, 34, 37, 38} {
		value, err := strconv.ParseFloat(strings.TrimSpace(fields[index]), 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
			return securityDetailView{}, nil, fmt.Errorf("报价字段%d无效", index)
		}
		values[index] = value
	}
	previous, open, high, low := values[4], values[5], values[33], values[34]
	if previous <= 0 || open < 0 || high < 0 || low < 0 || values[6] < 0 || values[37] < 0 || values[38] < 0 {
		return securityDetailView{}, nil, fmt.Errorf("报价数值超出有效范围")
	}
	if math.Abs((quote.Price-previous)-values[31]) > 0.011 ||
		math.Abs((quote.Price-previous)/previous*100-values[32]) > 0.011 {
		return securityDetailView{}, nil, fmt.Errorf("昨收、价格和涨跌幅不一致")
	}
	// 无成交时源端可能返回全零开高低，保留零表示尚无有效成交价，不补造价格。
	noTrades := open == 0 && high == 0 && low == 0 && values[6] == 0 && values[37] == 0
	if noTrades {
		if math.Abs(quote.Price-previous) > 0.001 {
			return securityDetailView{}, nil, fmt.Errorf("无成交报价与昨收不一致")
		}
	} else if low <= 0 || open <= 0 || high < low ||
		quote.Price < low-0.001 || quote.Price > high+0.001 || open < low-0.001 || open > high+0.001 {
		return securityDetailView{}, nil, fmt.Errorf("现价、今开和高低价不一致")
	}
	amountScale := float64(10000)
	var metadata hkquote.Metadata
	if isHongKongSecurity(item) {
		amountScale = 1
		metadata, err = hkquote.ParseTencentMetadata(fields)
		if err != nil {
			return securityDetailView{}, nil, err
		}
	}
	return securityDetailView{
		Currency: metadata.Currency, LotSize: metadata.LotSize, SecurityKind: metadata.Kind,
		SnapshotValidated: true, SyncWarning: stock.PriorityWarning(item.Symbol),
		ID: item.ID, Code: item.Code, Symbol: item.Symbol, Name: item.Name, Market: item.Market,
		Exchange: item.Exchange, Board: item.Board, LastPrice: quote.Price, PrevClose: previous,
		Open: open, High: high, Low: low, ChangeRate: values[32], Volume: values[6],
		Amount: values[37] * amountScale, Turnover: values[38], Source: quote.Source,
		QuoteAt: quote.At, UpdatedAt: now.Unix(),
	}, fields, nil
}
