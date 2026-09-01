package stock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm/clause"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

type eastmoneyResponse struct {
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
		} `json:"diff"`
	} `json:"data"`
}

type flexibleFloat float64

type eastmoneyDate string

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
	if err != nil {
		return nil
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
	UpdatedAt  int64   `json:"updated_at"`
}

func toResponse(item system.StockSecurity) securityResponse {
	return securityResponse{ID: item.ID, Code: item.Code, Symbol: item.Symbol, Market: item.Market, Name: item.Name, Exchange: item.Exchange, Board: item.Board, ListDate: item.ListDate, LastPrice: item.LastPrice, ChangeRate: item.ChangeRate, Status: item.Status, Source: item.Source, UpdatedAt: item.UpdatedAt.Unix()}
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
	response.ReturnData(c, gin.H{"synced": synced, "limit": 10000, "source": "Eastmoney public quote API"})
}

// SyncEastmoneyData 将东方财富公开行情接口的数据批量同步至本地缓存。
func SyncEastmoneyData(maxSyncRows int) (int, error) {
	client := &http.Client{Timeout: 12 * time.Second}
	synced := 0
	if maxSyncRows <= 0 || maxSyncRows > 10000 {
		maxSyncRows = 10000
	}
	for page, totalPages := 1, 1; page <= totalPages; page++ {
		q := url.Values{"pn": {fmt.Sprint(page)}, "pz": {"100"}, "po": {"1"}, "np": {"1"}, "ut": {"bd1d9ddb04089700cf9c27f6f7426281"}, "fltt": {"2"}, "invt": {"2"}, "fid": {"f3"}, "fs": {"m:0+t:6,m:0+t:80,m:1+t:2,m:1+t:23,m:0+t:81+s:2048"}, "fields": {"f12,f13,f14,f26,f2,f3,f5,f6,f8"}}
		resp, err := client.Get("https://push2delay.eastmoney.com/api/qt/clist/get?" + q.Encode())
		if err != nil {
			return synced, err
		}
		var payload eastmoneyResponse
		err = json.NewDecoder(resp.Body).Decode(&payload)
		resp.Body.Close()
		if err != nil || len(payload.Data.Diff) == 0 {
			if err != nil {
				return synced, err
			}
			break
		}
		if payload.Data.Total > 0 {
			totalPages = (payload.Data.Total + 99) / 100
			if totalPages > maxSyncRows/100 {
				totalPages = maxSyncRows / 100
			}
		}
		securities := make([]system.StockSecurity, 0, len(payload.Data.Diff))
		for _, item := range payload.Data.Diff {
			market := "A股"
			suffix := "SZ"
			exchange := "深交所"
			if item.Market == 1 {
				suffix = "SH"
				exchange = "上交所"
			}
			board := classifyBoard(item.Code, item.Market)
			if board == "北交所" {
				suffix = "BJ"
				exchange = "北交所"
			}
			securities = append(securities, system.StockSecurity{Code: item.Code, Symbol: item.Code + "." + suffix, Market: market, Name: item.Name, Exchange: exchange, Board: board, ListDate: string(item.ListDate), LastPrice: float64(item.Price), ChangeRate: float64(item.ChangeRate), Volume: float64(item.Volume), Amount: float64(item.Amount), Turnover: float64(item.Turnover), Status: system.StatusEnabled, Source: "eastmoney"})
		}
		if err := pgdb.GetClient().Clauses(clause.OnConflict{Columns: []clause.Column{{Name: "code"}}, DoUpdates: clause.AssignmentColumns([]string{"symbol", "market", "name", "exchange", "board", "list_date", "last_price", "change_rate", "volume", "amount", "turnover", "source", "updated_at"})}).Create(&securities).Error; err != nil {
			return synced, err
		}
		synced += len(securities)
	}
	return synced, nil
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
