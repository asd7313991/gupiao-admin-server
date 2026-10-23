package stock

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"api-server/util/hkquote"
)

const (
	hkListURL  = "https://vip.stock.finance.sina.com.cn/quotes_service/api/json_v2.php/Market_Center.getHKStockData"
	hkQuoteURL = "https://qt.gtimg.cn/"
)

var hkCodePattern = regexp.MustCompile(`^\d{5}$`)
var hkLocation = time.FixedZone("Asia/Hong_Kong", 8*60*60)

type hongKongSyncStatus struct {
	State       string `json:"state"`
	Stage       string `json:"stage"`
	Pages       int    `json:"pages"`
	Fetched     int    `json:"fetched"`
	Quoted      int    `json:"quoted"`
	Synced      int    `json:"synced"`
	Tencent     int    `json:"tencent"`
	Eastmoney   int    `json:"eastmoney"`
	Sina        int    `json:"sina"`
	Unavailable int    `json:"unavailable"`
	Error       string `json:"error"`
	StartedAt   int64  `json:"started_at"`
	FinishedAt  int64  `json:"finished_at"`
}

var hkSync = struct {
	sync.Mutex
	status hongKongSyncStatus
}{status: hongKongSyncStatus{State: "idle"}}

func HongKongSyncStatus(c *gin.Context) {
	hkSync.Lock()
	status := hkSync.status
	hkSync.Unlock()
	response.ReturnData(c, status)
}

func SyncHongKong(c *gin.Context) {
	if !beginHongKongSync() {
		response.ReturnError(c, response.FAILED_PRECONDITION, "港股采集正在执行，请勿重复提交")
		return
	}
	hkSync.Lock()
	status := hkSync.status
	hkSync.Unlock()

	go runHongKongSync()
	response.ReturnData(c, status)
}

func beginHongKongSync() bool {
	hkSync.Lock()
	defer hkSync.Unlock()
	if hkSync.status.State == "running" {
		return false
	}
	hkSync.status = hongKongSyncStatus{State: "running", Stage: "正在获取新浪港股列表", StartedAt: time.Now().Unix()}
	return true
}

func SyncHongKongData() error {
	if !beginHongKongSync() {
		return ErrHongKongSyncRunning
	}
	return runHongKongSync()
}

func runHongKongSync() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	progress := func(status hongKongSyncStatus) {
		hkSync.Lock()
		status.State, status.StartedAt = "running", hkSync.status.StartedAt
		hkSync.status = status
		hkSync.Unlock()
	}
	client := &http.Client{Timeout: 20 * time.Second}
	securities, result, err := collectHongKong(ctx, client, hkListURL, hkQuoteURL, hkquote.EastmoneyURL, progress)
	if err == nil {
		err = saveHongKongSecurities(ctx, pgdb.GetClient(), securities)
	}
	hkSync.Lock()
	result.StartedAt, result.FinishedAt = hkSync.status.StartedAt, time.Now().Unix()
	if err != nil {
		result.State, result.Stage, result.Error = "failed", "港股采集失败，未写入本次数据", err.Error()
		zap.L().Error("港股采集失败", zap.Error(err))
	} else {
		result.State, result.Stage, result.Synced = "success", "港股采集完成", len(securities)
		zap.L().Info("港股采集完成", zap.Int("synced", result.Synced), zap.Int("eastmoney", result.Eastmoney), zap.Int("tencent", result.Tencent), zap.Int("sina", result.Sina), zap.Int("unavailable", result.Unavailable))
	}
	hkSync.status = result
	hkSync.Unlock()
	return err
}

type sinaHongKongSecurity struct {
	Code       string `json:"symbol"`
	Name       string `json:"name"`
	Price      string `json:"lasttrade"`
	ChangeRate string `json:"changepercent"`
	Volume     string `json:"volume"`
	Amount     string `json:"amount"`
	QuoteTime  string `json:"ticktime"`
}

func collectHongKong(ctx context.Context, client *http.Client, listURL, quoteURL, eastmoneyURL string, progress func(hongKongSyncStatus)) ([]system.StockSecurity, hongKongSyncStatus, error) {
	result := hongKongSyncStatus{Stage: "正在获取新浪港股列表"}
	securities := make([]system.StockSecurity, 0, 3000)
	seen := make(map[string]bool)
	complete := false
	for page := 1; page <= 100; page++ {
		query := url.Values{"page": {strconv.Itoa(page)}, "num": {"60"}, "sort": {"symbol"}, "asc": {"1"}, "node": {"qbgg_hk"}, "_s_r_a": {"init"}}
		body, err := fetchHongKongBody(ctx, client, listURL+"?"+query.Encode(), "https://vip.stock.finance.sina.com.cn/mkt/", false)
		if err != nil {
			return nil, result, fmt.Errorf("新浪第 %d 页请求失败：%w", page, err)
		}
		var items []sinaHongKongSecurity
		if err := json.Unmarshal(body, &items); err != nil || strings.TrimSpace(string(body)) == "null" {
			return nil, result, fmt.Errorf("新浪第 %d 页返回无效的列表数据", page)
		}
		// 总数接口与实际列表不一致，以明确返回的空数组作为采集结束条件。
		if len(items) == 0 {
			complete = true
			break
		}
		if len(items) > 60 {
			return nil, result, fmt.Errorf("新浪第 %d 页超过请求条数", page)
		}
		for _, item := range items {
			if !hkCodePattern.MatchString(item.Code) || strings.TrimSpace(item.Name) == "" || seen[item.Code] {
				return nil, result, fmt.Errorf("新浪第 %d 页存在无效或重复证券代码：%s", page, item.Code)
			}
			if len(securities) > 0 && item.Code <= securities[len(securities)-1].Code {
				return nil, result, fmt.Errorf("新浪第 %d 页证券顺序异常：%s", page, item.Code)
			}
			security, err := sinaHongKongToSecurity(item)
			if err != nil {
				return nil, result, err
			}
			seen[item.Code] = true
			securities = append(securities, security)
		}
		result.Pages, result.Fetched = page, len(securities)
		progress(result)
		if err := waitHongKongRequest(ctx); err != nil {
			return nil, result, err
		}
	}
	if !complete || len(securities) == 0 {
		return nil, result, fmt.Errorf("新浪港股列表为空或超过采集安全上限，已取消写入")
	}

	result.Stage = "正在核对东方财富及腾讯港股报价"
	progress(result)
	for start := 0; start < len(securities); start += 100 {
		end := min(start+100, len(securities))
		if err := updateHongKongBatch(ctx, client, quoteURL, eastmoneyURL, securities[start:end]); err != nil {
			return nil, result, err
		}
		for index := start; index < end; index++ {
			security := &securities[index]
			switch security.Source {
			case "eastmoney":
				result.Eastmoney++
			case "sina/tencent":
				result.Tencent++
			case "sina":
				result.Sina++
			default:
				result.Unavailable++
			}
		}
		result.Quoted = end
		progress(result)
		if err := waitHongKongRequest(ctx); err != nil {
			return nil, result, err
		}
	}
	if result.Eastmoney+result.Tencent+result.Sina == 0 {
		return nil, result, fmt.Errorf("所有港股均无有效报价，已取消写入")
	}
	result.Stage = "正在保存港股数据"
	progress(result)
	return securities, result, nil
}

func fetchHongKongBody(ctx context.Context, client *http.Client, endpoint, referer string, gbk bool) ([]byte, error) {
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return nil, err
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	request.Header.Set("Referer", referer)
	resp, err := client.Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("行情接口响应状态异常：%d", resp.StatusCode)
	}
	var reader io.Reader = resp.Body
	if gbk {
		reader = transform.NewReader(reader, simplifiedchinese.GBK.NewDecoder())
	}
	const maxSize = 2 << 20
	body, err := io.ReadAll(io.LimitReader(reader, maxSize+1))
	if err != nil {
		return nil, err
	}
	if len(body) > maxSize {
		return nil, fmt.Errorf("行情接口响应超过大小限制")
	}
	return body, nil
}

func waitHongKongRequest(ctx context.Context) error {
	timer := time.NewTimer(500 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func hongKongNumber(text, field string) (float64, error) {
	text = strings.TrimSpace(text)
	if text == "" || text == "-" || text == "--" {
		return 0, nil
	}
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
		return 0, fmt.Errorf("港股%s数据无效：%q", field, text)
	}
	return value, nil
}

func sinaHongKongToSecurity(item sinaHongKongSecurity) (system.StockSecurity, error) {
	security := system.StockSecurity{Code: item.Code, Symbol: item.Code + ".HK", Name: item.Name, Market: "港股", Exchange: "港交所", Status: system.StatusEnabled, Source: "sina/unavailable"}
	fields := []struct {
		name string
		text string
		dest *float64
	}{
		{"价格", item.Price, &security.LastPrice},
		{"涨跌幅", item.ChangeRate, &security.ChangeRate},
		{"成交量", item.Volume, &security.Volume},
		{"成交额", item.Amount, &security.Amount},
	}
	for _, field := range fields {
		value, err := hongKongNumber(field.text, field.name)
		if err != nil {
			return security, fmt.Errorf("%s：%w", item.Code, err)
		}
		*field.dest = value
	}
	if security.LastPrice < 0 || security.Volume < 0 || security.Amount < 0 {
		return security, fmt.Errorf("%s：港股价格或成交数据不能为负数", item.Code)
	}
	if security.LastPrice > 0 {
		quoteTime, err := time.ParseInLocation("2006/01/02 15:04:05", item.QuoteTime, hkLocation)
		if err != nil {
			return security, fmt.Errorf("%s：新浪报价时间无效：%w", item.Code, err)
		}
		security.QuoteAt, security.Source = quoteTime.Unix(), "sina"
	}
	return security, nil
}

func parseHongKongTencentQuotes(body string) (map[string]system.StockSecurity, error) {
	quotes := make(map[string]system.StockSecurity)
	candidates := make(map[string]system.StockSecurity)
	seen := make(map[string]bool)
	var failures []error
	if !strings.Contains(body, "v_") {
		return nil, fmt.Errorf("腾讯港股行情响应格式无效")
	}
	for _, line := range strings.Split(body, ";") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "v_hk") && !strings.HasPrefix(line, "v_r_hk") {
			continue
		}
		separator := strings.Index(line, "=")
		if separator < 0 {
			failures = append(failures, fmt.Errorf("腾讯港股行情缺少分隔符"))
			continue
		}
		alias := line[:separator]
		if seen[alias] {
			delete(candidates, alias)
			failures = append(failures, fmt.Errorf("腾讯港股候选重复：%s", alias))
			continue
		}
		seen[alias] = true
		code := strings.TrimPrefix(strings.TrimPrefix(line[:separator], "v_"), "r_")
		code = strings.TrimPrefix(code, "hk")
		fields := strings.Split(strings.Trim(line[separator+1:], `"`), "~")
		if !hkCodePattern.MatchString(code) || len(fields) < 38 || fields[2] != code {
			failures = append(failures, fmt.Errorf("腾讯港股行情字段不完整或代码不匹配：%s", code))
			continue
		}
		price, err := hongKongNumber(fields[3], "价格")
		if err != nil || price < 0 {
			failures = append(failures, fmt.Errorf("腾讯港股价格无效：%s", code))
			continue
		}
		if price == 0 {
			continue
		}
		security, err := sinaHongKongToSecurity(sinaHongKongSecurity{Code: code, Price: fields[3], ChangeRate: fields[32], Volume: fields[6], Amount: fields[37], QuoteTime: fields[30]})
		if err != nil {
			failures = append(failures, fmt.Errorf("腾讯港股数据无效：%w", err))
			continue
		}
		if security.QuoteAt <= 0 || security.QuoteAt > time.Now().Unix()+60 {
			failures = append(failures, fmt.Errorf("腾讯港股时间无效或超前：%s", code))
			continue
		}
		if metadata, err := hkquote.ParseTencentMetadata(fields); err == nil {
			security.HKLotSize, security.HKCurrency, security.HKKind, security.HKMetadataAt = metadata.LotSize, metadata.Currency, metadata.Kind, metadata.At
		} else if len(fields) > 75 {
			failures = append(failures, fmt.Errorf("%s 证券资料无效：%w", code, err))
		}
		candidates[alias] = security
	}
	for alias, security := range candidates {
		if previous, ok := quotes[security.Code]; !ok || security.QuoteAt > previous.QuoteAt ||
			security.QuoteAt == previous.QuoteAt && strings.HasPrefix(alias, "v_r_hk") {
			quotes[security.Code] = security
		}
	}
	return quotes, errors.Join(failures...)
}

func hongKongSnapshotUpdates(now time.Time) clause.Set {
	updates := clause.AssignmentColumns([]string{"symbol", "market", "name", "exchange", "updated_at"})
	for _, column := range []string{"last_price", "change_rate", "volume", "amount", "quote_at", "source"} {
		updates = append(updates, clause.Assignment{
			Column: clause.Column{Name: column},
			Value:  gorm.Expr("CASE WHEN EXCLUDED.last_price > 0 AND EXCLUDED.quote_at > 0 AND EXCLUDED.quote_at <= ? AND EXCLUDED.quote_at >= stock_securities.quote_at THEN EXCLUDED."+column+" ELSE stock_securities."+column+" END", now.Unix()+60),
		})
	}
	for _, column := range []string{"hk_lot_size", "hk_currency", "hk_kind", "hk_metadata_at"} {
		updates = append(updates, clause.Assignment{Column: clause.Column{Name: column},
			Value: gorm.Expr("CASE WHEN EXCLUDED.hk_lot_size >= 1 AND EXCLUDED.hk_currency <> '' AND EXCLUDED.hk_kind <> '' AND EXCLUDED.hk_metadata_at > 0 AND EXCLUDED.hk_metadata_at <= ? AND EXCLUDED.hk_metadata_at >= stock_securities.hk_metadata_at THEN EXCLUDED."+column+" ELSE stock_securities."+column+" END", now.Unix()+60)})
	}
	return updates
}

func saveHongKongSecurities(ctx context.Context, db *gorm.DB, securities []system.StockSecurity) error {
	if len(securities) == 0 {
		return fmt.Errorf("港股证券列表为空，已取消写入")
	}
	securities = orderedQuoteRows(securities)
	return db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{
			Columns:   []clause.Column{{Name: "code"}},
			DoUpdates: hongKongSnapshotUpdates(time.Now()),
		}).CreateInBatches(&securities, 100).Error
	})
}
