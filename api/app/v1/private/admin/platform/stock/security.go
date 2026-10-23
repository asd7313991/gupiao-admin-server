package stock

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

type eastmoneyResponse struct {
	RC   int `json:"rc"`
	Data struct {
		Total int `json:"total"`
		Diff  []struct {
			Code       string        `json:"f12"`
			Market     int           `json:"f13"`
			Name       string        `json:"f14"`
			ListDate   eastmoneyDate `json:"f26"`
			Price      flexibleFloat `json:"f2"`
			ChangeRate flexibleFloat `json:"f3"`
			Volume     flexibleFloat `json:"f5"`
			Amount     flexibleFloat `json:"f6"`
			Turnover   flexibleFloat `json:"f8"`
			QuoteAt    int64         `json:"f124"`
		} `json:"diff"`
	} `json:"data"`
}

type flexibleFloat float64

type eastmoneyDate string

type tencentQuote struct {
	Price      float64
	ChangeRate float64
	QuoteAt    int64
	Volume     float64
	Amount     float64
	Turnover   float64
	MarketData bool
}

func (value *eastmoneyDate) UnmarshalJSON(data []byte) error {
	text := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if len(text) != 8 || text == "00000000" {
		*value = ""
		return nil
	}
	parsed, err := time.Parse("20060102", text)
	if err != nil {
		*value = ""
		return nil
	}
	*value = eastmoneyDate(parsed.Format("2006-01-02"))
	return nil
}

func (value *flexibleFloat) UnmarshalJSON(data []byte) error {
	text := strings.Trim(strings.TrimSpace(string(data)), `"`)
	if text == "" || text == "-" || text == "--" || text == "null" {
		*value = 0
		return nil
	}
	number, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return fmt.Errorf("行情数值无效：%q", text)
	}
	*value = flexibleFloat(number)
	return nil
}

type securityResponse struct {
	ID         uint    `json:"id"`
	Code       string  `json:"code"`
	Symbol     string  `json:"symbol"`
	Market     string  `json:"market"`
	Name       string  `json:"name"`
	Exchange   string  `json:"exchange"`
	Board      string  `json:"board"`
	ListDate   string  `json:"list_date"`
	LastPrice  float64 `json:"last_price"`
	ChangeRate float64 `json:"change_rate"`
	Status     uint    `json:"status"`
	Source     string  `json:"source"`
	QuoteAt    int64   `json:"quote_at"`
	UpdatedAt  int64   `json:"updated_at"`
}

func toResponse(item system.StockSecurity) securityResponse {
	return securityResponse{ID: item.ID, Code: item.Code, Symbol: item.Symbol, Market: item.Market, Name: item.Name, Exchange: item.Exchange, Board: item.Board, ListDate: item.ListDate, LastPrice: item.LastPrice, ChangeRate: item.ChangeRate, Status: item.Status, Source: item.Source, QuoteAt: item.QuoteAt, UpdatedAt: item.UpdatedAt.Unix()}
}

func List(c *gin.Context) {
	page, pageSize := middleware.GetPage(c), middleware.GetPageSize(c)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	db := pgdb.GetClient().Model(&system.StockSecurity{}).Where("deleted_at IS NULL")
	if symbol := c.Query("symbol"); symbol != "" {
		db = db.Where("symbol LIKE ?", "%"+symbol+"%")
	}
	if name := c.Query("name"); name != "" {
		db = db.Where("name LIKE ?", "%"+name+"%")
	}
	if market := c.Query("market"); market != "" {
		db = db.Where("market = ?", market)
	}
	if exchange := c.Query("exchange"); exchange != "" {
		db = db.Where("exchange = ?", exchange)
	}
	if board := c.Query("board"); board != "" {
		db = db.Where("board = ?", board)
	}
	// cursor 优先：大数据量翻页避免 OFFSET 扫描大量已跳过的记录。
	if cursor := c.Query("cursor"); cursor != "" {
		db = db.Where("id < ?", cursor)
	}
	sortField := "id"
	if c.Query("sort_by") == "id" {
		sortField = "id"
	}
	if c.Query("sort_by") == "last_price" {
		sortField = "last_price"
	}
	if c.Query("sort_by") == "change_rate" {
		sortField = "change_rate"
	}
	sortOrder := "DESC"
	if c.Query("sort_order") == "asc" {
		sortOrder = "ASC"
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询证券失败")
		return
	}
	var items []system.StockSecurity
	query := db.Order(sortField + " " + sortOrder + ", id DESC").Limit(pageSize)
	if c.Query("cursor") == "" {
		query = query.Offset((page - 1) * pageSize)
	}
	if err := query.Find(&items).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询证券失败")
		return
	}
	nextCursor := uint(0)
	if len(items) == pageSize {
		nextCursor = items[len(items)-1].ID
	}
	result := make([]securityResponse, len(items))
	for index, item := range items {
		result[index] = toResponse(item)
	}
	response.ReturnData(c, gin.H{"records": result, "total": total, "next_cursor": nextCursor})
}

func ListExchanges(c *gin.Context) {
	var exchanges []string
	if err := pgdb.GetClient().Model(&system.StockSecurity{}).Where("deleted_at IS NULL AND exchange <> ''").Distinct("exchange").Order("exchange ASC").Pluck("exchange", &exchanges).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询板块失败")
		return
	}
	response.ReturnData(c, exchanges)
}

func EnableHongKongSecurities(c *gin.Context) {
	updated, err := enableHongKongSecurities(pgdb.GetClient())
	if err != nil {
		zap.L().Error("批量启用港股失败", zap.Error(err))
		response.ReturnError(c, response.DATA_LOSS, "批量启用港股失败，请刷新后重试")
		return
	}
	response.ReturnData(c, gin.H{"updated": updated})
}

func enableHongKongSecurities(db *gorm.DB) (int64, error) {
	result := db.Model(&system.StockSecurity{}).
		Where("symbol LIKE ? AND status IS DISTINCT FROM ?", "%.HK", system.StatusEnabled).
		Update("status", system.StatusEnabled)
	return result.RowsAffected, result.Error
}
func ListBoards(c *gin.Context) {
	var boards []string
	if err := pgdb.GetClient().Model(&system.StockSecurity{}).Where("deleted_at IS NULL AND board <> ''").Distinct("board").Order("board ASC").Pluck("board", &boards).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询板块失败")
		return
	}
	response.ReturnData(c, boards)
}

func Save(c *gin.Context) {
	var input system.StockSecurity
	if !middleware.CheckParam(&input, c) || input.Code == "" || input.Name == "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "证券代码和名称为必填项")
		return
	}
	if input.Symbol == "" {
		input.Symbol = input.Code
	}
	if input.Status == 0 {
		input.Status = system.StatusEnabled
	}
	if input.ID == 0 {
		if err := pgdb.GetClient().Create(&input).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "添加证券失败")
			return
		}
	} else if err := pgdb.GetClient().Save(&input).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "修改证券失败")
		return
	}
	response.ReturnData(c, toResponse(input))
}
func Delete(c *gin.Context) {
	var input struct {
		ID uint `json:"id"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "证券 ID 无效")
		return
	}
	if err := pgdb.GetClient().Delete(&system.StockSecurity{}, input.ID).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "删除证券失败")
		return
	}
	response.ReturnData(c, nil)
}
func UpdateStatus(c *gin.Context) {
	var input struct {
		ID     uint `json:"id"`
		Status uint `json:"status"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "证券 ID 无效")
		return
	}
	if input.Status != system.StatusEnabled {
		input.Status = system.StatusDisabled
	}
	if err := pgdb.GetClient().Model(&system.StockSecurity{}).Where("id = ?", input.ID).Update("status", input.Status).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "更新证券状态失败")
		return
	}
	response.ReturnData(c, nil)
}

// SyncEastmoney 从东方财富公开接口同步 A 股证券基础数据，不需要 API Key。
func SyncEastmoney(c *gin.Context) {
	synced, err := SyncEastmoneyData(10000)
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "同步东方财富数据失败")
		return
	}
	response.ReturnData(c, gin.H{"synced": synced, "limit": 10000, "source": "Eastmoney/Tencent public quote API"})
}

// SyncEastmoneyData 将东方财富公开行情接口的数据批量同步至本地缓存。
func SyncEastmoneyData(maxSyncRows int) (int, error) {
	eastmoneySyncLock.Lock()
	defer eastmoneySyncLock.Unlock()
	client := &http.Client{Timeout: 12 * time.Second}
	securities, err := fetchEastmoneySnapshot(client, maxSyncRows)
	if err != nil {
		zap.L().Warn("东方财富全市场采集失败，尝试腾讯备用行情", zap.Error(err))
		var existing []system.StockSecurity
		limit := maxSyncRows
		if limit <= 0 || limit > 10000 {
			limit = 10000
		}
		db := pgdb.GetClient().Where("market = ?", "A股").Order("code").Limit(limit)
		if queryErr := db.Find(&existing).Error; queryErr != nil {
			return 0, fmt.Errorf("东方财富采集失败：%w；读取备用证券列表失败：%v", err, queryErr)
		}
		securities, err = fetchTencentSnapshot(client, existing)
		if err != nil {
			return 0, fmt.Errorf("腾讯全市场备用行情失败：%w", err)
		}
	}
	err = pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
		return tx.Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "code"}}, DoUpdates: clause.AssignmentColumns([]string{"symbol", "market", "name", "exchange", "board", "list_date", "last_price", "change_rate", "volume", "amount", "turnover", "source", "quote_at", "updated_at"})}).CreateInBatches(&securities, 100).Error
	})
	if err != nil {
		return 0, err
	}
	return len(securities), nil
}

func fillTencentPrices(client *http.Client, securities []system.StockSecurity) error {
	indexBySymbol := make(map[string]int)
	symbols := make([]string, 0)
	for index := range securities {
		if securities[index].LastPrice > 0 {
			continue
		}
		symbol := tencentSymbol(securities[index])
		indexBySymbol[symbol] = index
		symbols = append(symbols, symbol)
	}
	for start := 0; start < len(symbols); start += 100 {
		end := start + 100
		if end > len(symbols) {
			end = len(symbols)
		}
		quotes, err := fetchTencentBatch(context.Background(), client, symbols[start:end])
		if err != nil {
			return err
		}
		for symbol, quote := range quotes {
			index, exists := indexBySymbol[symbol]
			if !exists || quote.Price <= 0 {
				continue
			}
			securities[index].LastPrice = quote.Price
			securities[index].ChangeRate = quote.ChangeRate
			securities[index].QuoteAt = quote.QuoteAt
			if quote.MarketData {
				securities[index].Volume, securities[index].Amount, securities[index].Turnover = quote.Volume, quote.Amount, quote.Turnover
			}
			securities[index].Source = "eastmoney/tencent"
		}
	}
	return nil
}

func tencentSymbol(security system.StockSecurity) string {
	prefix := "sz"
	if security.Exchange == "上交所" {
		prefix = "sh"
	}
	if security.Exchange == "北交所" {
		prefix = "bj"
	}
	return prefix + security.Code
}

func parseTencentQuotes(body string) map[string]tencentQuote {
	quotes := make(map[string]tencentQuote)
	for _, line := range strings.Split(body, ";") {
		line = strings.TrimSpace(line)
		separator := strings.Index(line, "=")
		if separator <= 2 {
			continue
		}
		symbol := strings.TrimPrefix(line[:separator], "v_")
		fields := strings.Split(strings.Trim(line[separator+1:], `"`), "~")
		if len(fields) < 33 {
			continue
		}
		price, err := strconv.ParseFloat(fields[3], 64)
		if err != nil || price <= 0 || math.IsNaN(price) || math.IsInf(price, 0) || len(symbol) != 8 || fields[2] != symbol[2:] {
			continue
		}
		changeRate, err := strconv.ParseFloat(fields[32], 64)
		if err != nil || math.IsNaN(changeRate) || math.IsInf(changeRate, 0) {
			continue
		}
		quoteTime, err := time.ParseInLocation("20060102150405", fields[30], time.FixedZone("Asia/Shanghai", 8*60*60))
		if err != nil {
			continue
		}
		quote := tencentQuote{Price: price, ChangeRate: changeRate, QuoteAt: quoteTime.Unix()}
		if len(fields) >= 39 {
			stats := make([]float64, 3)
			valid := true
			for index, field := range []int{6, 37, 38} {
				text := fields[field]
				if text == "-" || text == "" {
					text = "0"
				}
				value, err := strconv.ParseFloat(text, 64)
				if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
					valid = false
					break
				}
				stats[index] = value
			}
			if !valid || math.IsInf(stats[1]*10000, 0) {
				continue
			}
			quote.Volume, quote.Amount, quote.Turnover, quote.MarketData = stats[0], stats[1]*10000, stats[2], true
		}
		quotes[symbol] = quote
	}
	return quotes
}

func classifyBoard(code string, market int) string {
	if market == 0 && (len(code) > 0 && (code[0] == '4' || code[0] == '8') || strings.HasPrefix(code, "920")) {
		return "北交所"
	}
	if len(code) >= 3 && code[:3] == "688" {
		return "科创板"
	}
	if len(code) >= 3 && (code[:3] == "300" || code[:3] == "301") {
		return "创业板"
	}
	return "主板"
}
