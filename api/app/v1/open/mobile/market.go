package mobile

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
	"gorm.io/gorm"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

type securityView struct {
	ID         uint    `json:"id"`
	Code       string  `json:"code"`
	Symbol     string  `json:"symbol"`
	Market     string  `json:"market"`
	Name       string  `json:"name"`
	Exchange   string  `json:"exchange"`
	Board      string  `json:"board"`
	LastPrice  float64 `json:"last_price"`
	ChangeRate float64 `json:"change_rate"`
	Volume     float64 `json:"volume"`
	Amount     float64 `json:"amount"`
	Turnover   float64 `json:"turnover"`
	UpdatedAt  int64   `json:"updated_at"`
}

type securityDetailView struct {
	ID         uint    `json:"id"`
	Code       string  `json:"code"`
	Symbol     string  `json:"symbol"`
	Market     string  `json:"market"`
	Name       string  `json:"name"`
	Exchange   string  `json:"exchange"`
	Board      string  `json:"board"`
	LastPrice  float64 `json:"last_price"`
	ChangeRate float64 `json:"change_rate"`
	Volume     float64 `json:"volume"`
	Amount     float64 `json:"amount"`
	Turnover   float64 `json:"turnover"`
	UpdatedAt  int64   `json:"updated_at"`
	Open       float64 `json:"open"`
	High       float64 `json:"high"`
	Low        float64 `json:"low"`
	PrevClose  float64 `json:"prev_close"`
}

type kLinePoint struct {
	Time     string  `json:"time"`
	Open     float64 `json:"open"`
	High     float64 `json:"high"`
	Low      float64 `json:"low"`
	Close    float64 `json:"close"`
	Volume   float64 `json:"volume"`
	Amount   float64 `json:"amount"`
	Turnover float64 `json:"turnover"`
}

type orderBookLevel struct {
	Price  float64 `json:"price"`
	Volume float64 `json:"volume"`
}

type orderBookView struct {
	Code       string           `json:"code"`
	Name       string           `json:"name"`
	LastPrice  float64          `json:"last_price"`
	PrevClose  float64          `json:"prev_close"`
	Open       float64          `json:"open"`
	Change     float64          `json:"change"`
	ChangeRate float64          `json:"change_rate"`
	LimitUp    float64          `json:"limit_up"`
	LimitDown  float64          `json:"limit_down"`
	Bid        []orderBookLevel `json:"bid"`
	Ask        []orderBookLevel `json:"ask"`
	UpdatedAt  string           `json:"updated_at"`
}

type tencentKLineStock struct {
	QFQDay   [][]json.RawMessage `json:"qfqday"`
	QFQWeek  [][]json.RawMessage `json:"qfqweek"`
	QFQMonth [][]json.RawMessage `json:"qfqmonth"`
	Day      [][]json.RawMessage `json:"day"`
	Week     [][]json.RawMessage `json:"week"`
	Month    [][]json.RawMessage `json:"month"`
	PreClose string              `json:"prec"`
}

type tencentKLineResponse struct {
	Code int                          `json:"code"`
	Data map[string]tencentKLineStock `json:"data"`
}

type tencentMinuteDay struct {
	Date string   `json:"date"`
	Data []string `json:"data"`
	Prec string   `json:"prec"`
}

type tencentMinuteResponse struct {
	Code int `json:"code"`
	Data map[string]struct {
		Data tencentMinuteDay `json:"data"`
	} `json:"data"`
}

type tencentFiveDayResponse struct {
	Code int `json:"code"`
	Data map[string]struct {
		Data []tencentMinuteDay `json:"data"`
	} `json:"data"`
}

func toSecurityView(item system.StockSecurity) securityView {
	return securityView{ID: item.ID, Code: item.Code, Symbol: item.Symbol, Market: item.Market, Name: item.Name, Exchange: item.Exchange, Board: item.Board, LastPrice: item.LastPrice, ChangeRate: item.ChangeRate, Volume: item.Volume, Amount: item.Amount, Turnover: item.Turnover, UpdatedAt: item.UpdatedAt.Unix()}
}

func ListSecurities(c *gin.Context) {
	page, pageSize := middleware.GetPage(c), middleware.GetPageSize(c)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	db := pgdb.GetClient().Model(&system.StockSecurity{}).Where("deleted_at IS NULL AND status = ?", system.StatusEnabled)
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		db = db.Where("code LIKE ? OR symbol LIKE ? OR name LIKE ?", "%"+keyword+"%", "%"+keyword+"%", "%"+keyword+"%")
	}
	for key, column := range map[string]string{"market": "market", "exchange": "exchange", "board": "board"} {
		if value := strings.TrimSpace(c.Query(key)); value != "" {
			db = db.Where(column+" = ?", value)
		}
	}
	sortFields := map[string]string{"change_rate": "change_rate", "volume": "volume", "amount": "amount", "turnover": "turnover"}
	sortField := sortFields[c.Query("sort_by")]
	if sortField == "" {
		sortField = "change_rate"
	}
	sortOrder := "DESC"
	if c.Query("sort_order") == "asc" {
		sortOrder = "ASC"
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取行情失败")
		return
	}
	var items []system.StockSecurity
	if err := db.Order(sortField + " " + sortOrder + ", id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取行情失败")
		return
	}
	records := make([]securityView, len(items))
	for index, item := range items {
		records[index] = toSecurityView(item)
	}
	response.ReturnData(c, gin.H{"records": records, "total": total, "page": page, "page_size": pageSize})
}

func SecurityDetail(c *gin.Context) {
	var item system.StockSecurity
	code := strings.TrimSpace(c.Param("code"))
	err := pgdb.GetClient().Where("code = ? OR symbol = ?", code, code).First(&item).Error
	if err == gorm.ErrRecordNotFound {
		response.ReturnError(c, response.NOT_FOUND, "证券不存在")
		return
	}
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取证券详情失败")
		return
	}
	detail := securityDetailView{ID: item.ID, Code: item.Code, Symbol: item.Symbol, Market: item.Market, Name: item.Name, Exchange: item.Exchange, Board: item.Board, LastPrice: item.LastPrice, ChangeRate: item.ChangeRate, Volume: item.Volume, Amount: item.Amount, Turnover: item.Turnover, UpdatedAt: item.UpdatedAt.Unix()}
	points, previousClose, fetchErr := fetchKLines(item, "day", 2)
	if fetchErr == nil && len(points) > 0 {
		latest := points[len(points)-1]
		detail.Open, detail.High, detail.Low = latest.Open, latest.High, latest.Low
		detail.Volume, detail.Amount = latest.Volume, latest.Amount
		detail.PrevClose = previousClose
	}
	minutePoints, minuteErr := fetchMinuteLines(item, false)
	if minuteErr == nil && len(minutePoints) > 0 {
		detail.Open = minutePoints[0].Open
		detail.High, detail.Low = minutePoints[0].High, minutePoints[0].Low
		detail.Volume, detail.Amount = 0, 0
		for _, point := range minutePoints {
			if point.High > detail.High {
				detail.High = point.High
			}
			if point.Low < detail.Low {
				detail.Low = point.Low
			}
			detail.Volume += point.Volume
			detail.Amount += point.Amount
		}
		detail.LastPrice = minutePoints[len(minutePoints)-1].Close
	}
	response.ReturnData(c, detail)
}

func SecurityKLines(c *gin.Context) {
	var item system.StockSecurity
	code := strings.TrimSpace(c.Param("code"))
	if err := pgdb.GetClient().Where("code = ? OR symbol = ?", code, code).First(&item).Error; err != nil {
		response.ReturnError(c, response.NOT_FOUND, "证券不存在")
		return
	}
	period := c.DefaultQuery("period", "day")
	limit, _ := strconv.Atoi(c.DefaultQuery("limit", "120"))
	if limit < 20 {
		limit = 20
	}
	if limit > 800 {
		limit = 800
	}
	var points []kLinePoint
	var err error
	if period == "minute" || period == "five_day" {
		points, err = fetchMinuteLines(item, period == "five_day")
	} else {
		points, _, err = fetchKLines(item, period, limit)
	}
	if err != nil {
		response.ReturnError(c, response.UNAVAILABLE, "K 线数据暂时不可用")
		return
	}
	response.ReturnData(c, gin.H{"code": item.Code, "period": period, "records": points})
}

func SecurityOrderBook(c *gin.Context) {
	var item system.StockSecurity
	code := strings.TrimSpace(c.Param("code"))
	if err := pgdb.GetClient().Where("code = ? OR symbol = ?", code, code).First(&item).Error; err != nil {
		response.ReturnError(c, response.NOT_FOUND, "证券不存在")
		return
	}
	book, err := fetchOrderBook(item)
	if err != nil {
		response.ReturnError(c, response.UNAVAILABLE, "盘口数据暂时不可用")
		return
	}
	response.ReturnData(c, book)
}

func fetchOrderBook(item system.StockSecurity) (orderBookView, error) {
	symbol := quoteSymbol(item)
	request, _ := http.NewRequest(http.MethodGet, "https://qt.gtimg.cn/q="+url.QueryEscape(symbol), nil)
	request.Header.Set("User-Agent", "Mozilla/5.0")
	request.Header.Set("Referer", "https://gu.qq.com/")
	result, err := (&http.Client{Timeout: 8 * time.Second}).Do(request)
	if err != nil {
		return orderBookView{}, err
	}
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK {
		return orderBookView{}, fmt.Errorf("orderbook status %d", result.StatusCode)
	}
	reader := transform.NewReader(result.Body, simplifiedchinese.GBK.NewDecoder())
	body, err := io.ReadAll(reader)
	if err != nil {
		return orderBookView{}, err
	}
	fields := strings.Split(string(body), "~")
	if len(fields) < 33 || number(fields[3]) <= 0 {
		return orderBookView{}, fmt.Errorf("invalid orderbook response")
	}
	book := orderBookView{Code: item.Code, Name: fields[1], LastPrice: number(fields[3]), PrevClose: number(fields[4]), Open: number(fields[5]), Change: number(fields[31]), ChangeRate: number(fields[32]), UpdatedAt: fields[30], Bid: make([]orderBookLevel, 0, 5), Ask: make([]orderBookLevel, 0, 5)}
	for index := 0; index < 5; index++ {
		book.Bid = append(book.Bid, orderBookLevel{Price: number(fields[9+index*2]), Volume: number(fields[10+index*2])})
		book.Ask = append(book.Ask, orderBookLevel{Price: number(fields[19+index*2]), Volume: number(fields[20+index*2])})
	}
	limitRate := 0.1
	if item.Board == "创业板" || item.Board == "科创板" {
		limitRate = 0.2
	}
	if item.Board == "北交所" {
		limitRate = 0.3
	}
	if strings.HasPrefix(item.Name, "ST") || strings.HasPrefix(item.Name, "*ST") {
		limitRate = 0.05
	}
	book.LimitUp = math.Round(book.PrevClose*(1+limitRate)*100) / 100
	book.LimitDown = math.Round(book.PrevClose*(1-limitRate)*100) / 100
	return book, nil
}

func fetchMinuteLines(item system.StockSecurity, fiveDays bool) ([]kLinePoint, error) {
	symbol := quoteSymbol(item)
	path := "minute/query"
	if fiveDays {
		path = "day/query"
	}
	request, _ := http.NewRequest(http.MethodGet, "https://web.ifzq.gtimg.cn/appstock/app/"+path+"?"+url.Values{"code": {symbol}}.Encode(), nil)
	request.Header.Set("User-Agent", "Mozilla/5.0")
	result, err := (&http.Client{Timeout: 12 * time.Second}).Do(request)
	if err != nil {
		return nil, err
	}
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("minute quote status %d", result.StatusCode)
	}
	days := make([]tencentMinuteDay, 0, 5)
	if fiveDays {
		var payload tencentFiveDayResponse
		if err := json.NewDecoder(result.Body).Decode(&payload); err != nil || payload.Code != 0 {
			return nil, fmt.Errorf("invalid five day response")
		}
		days = payload.Data[symbol].Data
	} else {
		var payload tencentMinuteResponse
		if err := json.NewDecoder(result.Body).Decode(&payload); err != nil || payload.Code != 0 {
			return nil, fmt.Errorf("invalid minute response")
		}
		days = append(days, payload.Data[symbol].Data)
	}
	points := make([]kLinePoint, 0, len(days)*240)
	for _, day := range days {
		date := day.Date
		if len(date) == 8 {
			date = date[:4] + "-" + date[4:6] + "-" + date[6:]
		}
		previousVolume, previousAmount := float64(0), float64(0)
		for _, line := range day.Data {
			fields := strings.Fields(line)
			if len(fields) < 3 || len(fields[0]) != 4 {
				continue
			}
			price, cumulativeVolume := number(fields[1]), number(fields[2])
			volume := cumulativeVolume - previousVolume
			if volume < 0 {
				volume = cumulativeVolume
			}
			amount := volume * price * 100
			if len(fields) >= 4 {
				cumulativeAmount := number(fields[3])
				amount = cumulativeAmount - previousAmount
				if amount < 0 {
					amount = cumulativeAmount
				}
				previousAmount = cumulativeAmount
			}
			previousVolume = cumulativeVolume
			points = append(points, kLinePoint{Time: date + " " + fields[0][:2] + ":" + fields[0][2:], Open: price, High: price, Low: price, Close: price, Volume: volume, Amount: amount, Turnover: amount})
		}
	}
	if len(points) == 0 {
		return nil, fmt.Errorf("minute quote data not found")
	}
	return points, nil
}

func fetchKLines(item system.StockSecurity, period string, limit int) ([]kLinePoint, float64, error) {
	if period != "day" && period != "week" && period != "month" {
		return nil, 0, fmt.Errorf("unsupported period")
	}
	symbol := quoteSymbol(item)
	query := url.Values{"param": {fmt.Sprintf("%s,%s,,,%d,qfq", symbol, period, limit)}}
	client := &http.Client{Timeout: 12 * time.Second}
	request, _ := http.NewRequest(http.MethodGet, "https://web.ifzq.gtimg.cn/appstock/app/fqkline/get?"+query.Encode(), nil)
	request.Header.Set("User-Agent", "Mozilla/5.0")
	result, err := client.Do(request)
	if err != nil {
		return nil, 0, err
	}
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("quote status %d", result.StatusCode)
	}
	var payload tencentKLineResponse
	if err := json.NewDecoder(result.Body).Decode(&payload); err != nil || payload.Code != 0 {
		return nil, 0, fmt.Errorf("invalid quote response")
	}
	stock, exists := payload.Data[symbol]
	if !exists {
		return nil, 0, fmt.Errorf("quote data not found")
	}
	rows := stock.QFQDay
	if period == "week" {
		rows = stock.QFQWeek
	}
	if period == "month" {
		rows = stock.QFQMonth
	}
	if len(rows) == 0 {
		rows = stock.Day
		if period == "week" {
			rows = stock.Week
		}
		if period == "month" {
			rows = stock.Month
		}
	}
	points := make([]kLinePoint, 0, len(rows))
	for _, fields := range rows {
		if len(fields) < 6 {
			continue
		}
		points = append(points, kLinePoint{Time: rawString(fields[0]), Open: rawNumber(fields[1]), Close: rawNumber(fields[2]), High: rawNumber(fields[3]), Low: rawNumber(fields[4]), Volume: rawNumber(fields[5]), Amount: optionalRawNumber(fields, 6)})
	}
	if len(points) == 0 {
		return nil, 0, fmt.Errorf("quote data not found")
	}
	return points, number(stock.PreClose), nil
}

func quoteSymbol(item system.StockSecurity) string {
	prefix := "sz"
	if item.Exchange == "上交所" {
		prefix = "sh"
	}
	if item.Exchange == "北交所" {
		prefix = "bj"
	}
	return prefix + item.Code
}

func number(value string) float64 {
	result, _ := strconv.ParseFloat(value, 64)
	return result
}

func rawString(value json.RawMessage) string {
	var result string
	_ = json.Unmarshal(value, &result)
	return result
}

func rawNumber(value json.RawMessage) float64 {
	return number(rawString(value))
}

func optionalRawNumber(values []json.RawMessage, index int) float64 {
	if index >= len(values) {
		return 0
	}
	return rawNumber(values[index])
}
