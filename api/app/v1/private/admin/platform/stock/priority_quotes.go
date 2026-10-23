package stock

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"api-server/api/response"
	"api-server/db/pgdb/system"
	"api-server/util/hkquote"
	"api-server/util/positionquote"
)

const priorityBatchSize = 100

type PriorityQuoteStatus struct {
	Market              string   `json:"market"`
	State               string   `json:"state"`
	LastAttempt         int64    `json:"last_attempt"`
	LastSuccess         int64    `json:"last_success"`
	NextAttempt         int64    `json:"next_attempt"`
	Targets             int      `json:"targets"`
	Checked             int      `json:"checked"`
	Updated             int      `json:"updated"`
	Unavailable         int      `json:"unavailable"`
	Delayed             int      `json:"delayed"`
	Deferred            int      `json:"deferred"`
	Eastmoney           int      `json:"eastmoney"`
	Tencent             int      `json:"tencent"`
	ConsecutiveFailures int      `json:"consecutive_failures"`
	Error               string   `json:"error"`
	SourceErrors        []string `json:"source_errors"`
	Symbols             []string `json:"symbols"`
}

type priorityJob struct {
	sync.Mutex
	running  sync.Mutex
	status   PriorityQuoteStatus
	warnings map[string]string
	cursor   string
	lastLog  time.Time
}

var priorityJobs = map[string]*priorityJob{
	"A":  {status: PriorityQuoteStatus{Market: "A", State: "waiting"}, warnings: make(map[string]string)},
	"HK": {status: PriorityQuoteStatus{Market: "HK", State: "waiting"}, warnings: make(map[string]string)},
}

var priorityStartedAt = time.Now()

var priorityActive = struct {
	sync.Mutex
	items map[string]time.Time
}{items: make(map[string]time.Time)}

func TouchPrioritySecurity(symbol string) {
	if positionquote.Symbol(symbol) == "" {
		return
	}
	now := time.Now()
	priorityActive.Lock()
	defer priorityActive.Unlock()
	for key, at := range priorityActive.items {
		if now.Sub(at) >= 2*time.Minute {
			delete(priorityActive.items, key)
		}
	}
	if _, exists := priorityActive.items[symbol]; !exists && len(priorityActive.items) >= 2000 {
		oldest, oldestAt := "", now
		for key, at := range priorityActive.items {
			if at.Before(oldestAt) {
				oldest, oldestAt = key, at
			}
		}
		delete(priorityActive.items, oldest)
	}
	priorityActive.items[symbol] = now
}

func activePrioritySymbols(now time.Time) []string {
	priorityActive.Lock()
	defer priorityActive.Unlock()
	symbols := make([]string, 0, len(priorityActive.items))
	for symbol, at := range priorityActive.items {
		if now.Sub(at) >= 2*time.Minute {
			delete(priorityActive.items, symbol)
		} else {
			symbols = append(symbols, symbol)
		}
	}
	sort.Strings(symbols)
	return symbols
}

func priorityTargets(db *gorm.DB, market string, active []string) *gorm.DB {
	query := db.Model(&system.StockSecurity{})
	if market == "HK" {
		query = query.Where("symbol LIKE ?", "%.HK")
	} else {
		query = query.Where("(symbol LIKE ? OR symbol LIKE ? OR symbol LIKE ?)", "%.SH", "%.SZ", "%.BJ")
	}
	condition := `EXISTS (SELECT 1 FROM trade_positions p WHERE p.symbol = stock_securities.symbol AND p.position_qty > 0 AND p.deleted_at IS NULL)
		OR EXISTS (SELECT 1 FROM customer_watchlists w WHERE w.security_id = stock_securities.id AND w.deleted_at IS NULL)`
	if len(active) > 0 {
		return query.Where("("+condition+" OR symbol IN ?)", active).Order("symbol")
	}
	return query.Where("(" + condition + ")").Order("symbol")
}

func priorityBatch(items []system.StockSecurity, cursor string) []system.StockSecurity {
	if len(items) <= priorityBatchSize {
		return items
	}
	start := sort.Search(len(items), func(index int) bool { return items[index].Symbol > cursor })
	if start == len(items) {
		start = 0
	}
	batch := make([]system.StockSecurity, 0, priorityBatchSize)
	for index := 0; index < priorityBatchSize; index++ {
		batch = append(batch, items[(start+index)%len(items)])
	}
	return batch
}

func priorityDelay(interval, failures int) time.Duration {
	if failures > 1 {
		interval *= 1 << min(failures-1, 4)
	}
	return time.Duration(min(interval, 60)) * time.Second
}

func PriorityStatuses(now time.Time) []PriorityQuoteStatus {
	result := make([]PriorityQuoteStatus, 0, 2)
	for _, market := range []string{"A", "HK"} {
		job := priorityJobs[market]
		job.Lock()
		status := job.status
		status.SourceErrors = append([]string{}, status.SourceErrors...)
		status.Symbols = append([]string{}, status.Symbols...)
		job.Unlock()
		if priorityStalled(status, now) {
			status.State, status.Error = "stalled", "重点行情任务长时间未完成或未运行，请检查后端任务与数据源"
		}
		result = append(result, status)
	}
	return result
}

func priorityStalled(status PriorityQuoteStatus, now time.Time) bool {
	if status.State == "disabled" {
		return false
	}
	last := max(status.NextAttempt, status.LastAttempt)
	if last == 0 {
		last = priorityStartedAt.Unix()
	}
	return now.Unix() > last+45
}

func PrioritySyncStatus(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	response.ReturnData(c, PriorityStatuses(time.Now()))
}

func PriorityWarning(symbol string) string {
	market := "A"
	if strings.HasSuffix(symbol, ".HK") {
		market = "HK"
	}
	job := priorityJobs[market]
	job.Lock()
	warning, status := job.warnings[symbol], job.status
	job.Unlock()
	if status.State == "disabled" {
		return "自动行情同步已关闭，请检查后台同步开关及原始报价时间"
	}
	if priorityStalled(status, time.Now()) {
		return "重点行情同步任务长时间未更新，请检查后台；当前交易仍须有效报价"
	}
	if warning != "" {
		return warning
	}
	if status.State == "failed" {
		return "重点行情同步失败，保留原始报价时间；请稍后重试或联系管理员"
	}
	return ""
}

func SetPrioritySyncEnabled(enabled bool) {
	for _, job := range priorityJobs {
		job.Lock()
		if !enabled {
			job.status.State, job.status.Error = "disabled", ""
		} else if job.status.State == "disabled" {
			job.status.State, job.status.NextAttempt = "waiting", 0
		}
		job.Unlock()
	}
}

func PriorityConfigError(err error) {
	for _, job := range priorityJobs {
		job.Lock()
		previous := job.status
		job.status.State, job.status.Error, job.status.LastAttempt = "failed", err.Error(), time.Now().Unix()
		job.status.NextAttempt = time.Now().Add(time.Minute).Unix()
		if previous.Error != job.status.Error || time.Since(job.lastLog) >= time.Minute {
			zap.L().Warn("重点行情同步配置读取失败", zap.String("market", job.status.Market), zap.Error(err))
			job.lastLog = time.Now()
		}
		job.Unlock()
	}
}

func RefreshPriorityQuotes(db *gorm.DB, market string, now time.Time, interval int) error {
	job, ok := priorityJobs[market]
	if !ok || interval <= 0 {
		return fmt.Errorf("重点行情市场或间隔无效")
	}
	if !job.running.TryLock() {
		return nil
	}
	defer job.running.Unlock()
	job.Lock()
	if now.Unix() < job.status.NextAttempt {
		job.Unlock()
		return nil
	}
	previous, cursor := job.status, job.cursor
	status := PriorityQuoteStatus{Market: market, State: "running", LastAttempt: now.Unix(), LastSuccess: previous.LastSuccess}
	job.status = status
	job.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var targets []system.StockSecurity
	err := priorityTargets(db.WithContext(ctx), market, activePrioritySymbols(now)).Find(&targets).Error
	var valid []system.StockSecurity
	var missing []string
	if err == nil {
		batch := priorityBatch(targets, cursor)
		status.Targets, status.Checked, status.Deferred = len(targets), len(batch), len(targets)-len(batch)
		if len(batch) > 0 {
			cursor = batch[len(batch)-1].Symbol
			valid, missing, status.SourceErrors = fetchPriorityBatch(ctx, &http.Client{Timeout: 8 * time.Second},
				hkQuoteURL, hkquote.EastmoneyURL, batch, now)
			if len(valid) == 0 {
				err = fmt.Errorf("本轮%d只重点股票均未取得通过时效检查的报价", len(batch))
			} else {
				err = savePriorityQuotes(ctx, db, valid)
			}
		}
	}
	status.Unavailable, status.Symbols = len(missing), append([]string{}, missing[:min(10, len(missing))]...)
	status.State = "success"
	delayed := make([]string, 0)
	if err != nil {
		status.State, status.Error = "failed", err.Error()
		status.ConsecutiveFailures = previous.ConsecutiveFailures + 1
	} else {
		status.Updated, status.LastSuccess = len(valid), time.Now().Unix()
		for _, item := range valid {
			if priorityQuoteDelayed(item, now) {
				delayed = append(delayed, item.Symbol)
			}
			if item.Source == "eastmoney" {
				status.Eastmoney++
			} else {
				status.Tencent++
			}
		}
		status.Delayed = len(delayed)
		if len(missing) > 0 || len(delayed) > 0 || len(status.SourceErrors) > 0 {
			status.State = "degraded"
		}
	}
	status.NextAttempt = time.Now().Add(priorityDelay(interval, status.ConsecutiveFailures)).Unix()
	job.Lock()
	job.cursor = cursor
	active := make(map[string]bool, len(targets))
	for _, item := range targets {
		active[item.Symbol] = true
	}
	if err == nil {
		for _, item := range valid {
			delete(job.warnings, item.Symbol)
		}
	}
	for symbol := range job.warnings {
		if !active[symbol] {
			delete(job.warnings, symbol)
		}
	}
	for _, symbol := range missing {
		job.warnings[symbol] = symbol + " 主备源均无有效新报价，保留旧报价时间，不用于成交或资产估值"
	}
	for _, symbol := range delayed {
		job.warnings[symbol] = symbol + " 原始报价已延迟超过3分钟，可能为无新成交或源端延迟；交易仍须通过时效检查"
	}
	if status.State != previous.State || status.Error != previous.Error || time.Since(job.lastLog) >= time.Minute {
		if status.State == "failed" || status.State == "degraded" {
			zap.L().Warn("重点行情同步异常", zap.String("market", market), zap.String("state", status.State),
				zap.String("error", status.Error), zap.Strings("source_errors", status.SourceErrors),
				zap.Int("unavailable", status.Unavailable), zap.Strings("symbols", status.Symbols))
		} else if previous.State == "failed" || previous.State == "degraded" {
			zap.L().Info("重点行情同步已恢复", zap.String("market", market))
		}
		job.lastLog = time.Now()
	}
	if job.status.State != "disabled" {
		job.status = status
	}
	job.Unlock()
	return err
}

func priorityQuoteDelayed(item system.StockSecurity, now time.Time) bool {
	current := now.In(time.FixedZone("UTC+8", 8*3600))
	minute, morningEnd, closeMinute := current.Hour()*60+current.Minute(), 11*60+30, 15*60
	if strings.HasSuffix(item.Symbol, ".HK") {
		morningEnd, closeMinute = 12*60, 16*60
	}
	trading := current.Weekday() != time.Saturday && current.Weekday() != time.Sunday &&
		((minute >= 570 && minute < morningEnd) || (minute >= 780 && minute < closeMinute))
	return trading && now.Unix()-item.QuoteAt > 180
}

func fetchPriorityBatch(ctx context.Context, client *http.Client, tencentURL, eastmoneyURL string, items []system.StockSecurity, now time.Time) ([]system.StockSecurity, []string, []string) {
	symbols, codes := make([]string, 0, len(items)), make([]string, 0, len(items))
	for _, item := range items {
		symbols = append(symbols, item.Symbol)
		codes = append(codes, positionquote.TencentCodes(item.Symbol)...)
	}
	var body []byte
	var eastmoney map[string]hkquote.Quote
	var tencentErr, eastmoneyErr error
	var workers sync.WaitGroup
	workers.Add(2)
	go func() {
		defer workers.Done()
		body, tencentErr = fetchHongKongBody(ctx, client, tencentURL+"?q="+url.QueryEscape(strings.Join(codes, ",")), "https://gu.qq.com/", true)
	}()
	go func() {
		defer workers.Done()
		eastmoney, eastmoneyErr = hkquote.FetchEastmoneySymbols(ctx, client, eastmoneyURL, symbols)
	}()
	workers.Wait()
	aQuotes := parseTencentQuotes(string(body))
	hkQuotes := make(map[string]system.StockSecurity)
	if len(items) > 0 && strings.HasSuffix(items[0].Symbol, ".HK") && tencentErr == nil {
		hkQuotes, tencentErr = parseHongKongTencentQuotes(string(body))
	}
	sourceErrors := make([]string, 0, 2)
	if tencentErr != nil {
		sourceErrors = append(sourceErrors, "腾讯："+tencentErr.Error())
	}
	if eastmoneyErr != nil {
		sourceErrors = append(sourceErrors, "东方财富："+eastmoneyErr.Error())
	}
	valid, missing := make([]system.StockSecurity, 0, len(items)), make([]string, 0)
	for _, item := range items {
		hk := strings.HasSuffix(item.Symbol, ".HK")
		best := system.StockSecurity{}
		if hk {
			best = hkQuotes[item.Code]
		} else if quote, ok := aQuotes[positionquote.Symbol(item.Symbol)]; ok && quote.MarketData {
			best.LastPrice, best.ChangeRate, best.Volume, best.Amount, best.QuoteAt = quote.Price, quote.ChangeRate, quote.Volume, quote.Amount, quote.QuoteAt
		}
		if best.LastPrice <= 0 || positionquote.ValidateTime(best.QuoteAt, now, hk) != nil {
			best = system.StockSecurity{}
		} else {
			best.Source = "tencent"
		}
		if quote, ok := eastmoney[item.Symbol]; ok && positionquote.ValidateTime(quote.QuoteAt, now, hk) == nil && quote.QuoteAt >= best.QuoteAt {
			best.LastPrice, best.ChangeRate, best.Volume, best.Amount, best.QuoteAt, best.Source =
				quote.LastPrice, quote.ChangeRate, quote.Volume, quote.Amount, quote.QuoteAt, "eastmoney"
		}
		if best.LastPrice <= 0 || ctx.Err() != nil {
			missing = append(missing, item.Symbol)
			continue
		}
		item.LastPrice, item.ChangeRate, item.Volume, item.Amount, item.QuoteAt, item.Source =
			best.LastPrice, best.ChangeRate, best.Volume, best.Amount, best.QuoteAt, best.Source
		valid = append(valid, item)
	}
	return valid, missing, sourceErrors
}

func orderedQuoteRows(items []system.StockSecurity) []system.StockSecurity {
	ordered := append([]system.StockSecurity(nil), items...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Code < ordered[j].Code })
	return ordered
}

func savePriorityQuotes(ctx context.Context, db *gorm.DB, items []system.StockSecurity) error {
	items = orderedQuoteRows(items)
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		for _, item := range items {
			if err := tx.Model(&system.StockSecurity{}).Where("id = ? AND quote_at <= ?", item.ID, item.QuoteAt).
				Updates(map[string]any{"last_price": item.LastPrice, "change_rate": item.ChangeRate,
					"volume": item.Volume, "amount": item.Amount, "quote_at": item.QuoteAt, "source": item.Source}).Error; err != nil {
				return err
			}
		}
		return nil
	})
}
