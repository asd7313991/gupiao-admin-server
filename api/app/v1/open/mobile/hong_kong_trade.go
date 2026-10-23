package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	stock "api-server/api/app/v1/private/admin/platform/stock"
	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/config"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"api-server/util/hkquote"
)

type hongKongQuote struct {
	SyncWarning string           `json:"sync_warning,omitempty"`
	Code        string           `json:"code"`
	LastPrice   float64          `json:"last_price"`
	ChangeRate  float64          `json:"change_rate"`
	LotSize     float64          `json:"lot_size"`
	Currency    string           `json:"currency"`
	Kind        string           `json:"kind"`
	QuoteAt     int64            `json:"quote_at"`
	DepthAt     int64            `json:"depth_at"`
	Source      string           `json:"source"`
	Bid         []orderBookLevel `json:"bid"`
	Ask         []orderBookLevel `json:"ask"`
}

func tradeMarketScope(db *gorm.DB, market string) *gorm.DB {
	if market == "HK" {
		return db.Where("symbol LIKE ?", "%.HK")
	}
	return db.Where("COALESCE(symbol, '') NOT LIKE ?", "%.HK")
}

func fetchHongKongTradeQuote(ctx context.Context, code string) (hongKongQuote, error) {
	return fetchHongKongTradeQuoteWithSources(ctx, &http.Client{Timeout: 8 * time.Second}, code, "https://qt.gtimg.cn/q=hk", hkquote.EastmoneyURL)
}

func fetchHongKongTradeQuoteWithSources(ctx context.Context, client *http.Client, code, tencentURL, eastmoneyURL string) (hongKongQuote, error) {
	if len(code) != 5 {
		return hongKongQuote{}, fmt.Errorf("港股代码必须为五位数字")
	}
	for _, digit := range code {
		if digit < '0' || digit > '9' {
			return hongKongQuote{}, fmt.Errorf("港股代码无效")
		}
	}
	// 腾讯提供每手、币种、品种和盘口，东方财富仅替换时间较新的有效价格，不推测交易元数据。
	quote, err := fetchTencentHongKongTradeQuote(ctx, client, code, tencentURL)
	if err != nil {
		return hongKongQuote{}, fmt.Errorf("读取港股交易元数据失败：%w", err)
	}
	quotes, err := hkquote.FetchEastmoney(ctx, client, eastmoneyURL, []string{code})
	if ctx.Err() != nil {
		return hongKongQuote{}, ctx.Err()
	}
	if err != nil {
		zap.L().Warn("东方财富港股报价失败，保留腾讯报价及原始时间", zap.String("code", code), zap.Error(err))
	} else if primary, ok := quotes[code]; ok && (primary.QuoteAt >= quote.QuoteAt || quote.LastPrice == 0 || quote.QuoteAt > time.Now().Unix()+60) {
		quote.LastPrice, quote.ChangeRate, quote.QuoteAt, quote.Source = primary.LastPrice, primary.ChangeRate, primary.QuoteAt, "eastmoney"
	} else if !ok {
		zap.L().Warn("东方财富港股无有效报价，保留腾讯报价及原始时间", zap.String("code", code))
	}
	if quote.LastPrice <= 0 {
		return hongKongQuote{}, fmt.Errorf("东方财富与腾讯均未返回有效港股价格")
	}
	return quote, nil
}

func fetchTencentHongKongTradeQuote(ctx context.Context, client *http.Client, code, endpoint string) (hongKongQuote, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+code, nil)
	if err != nil {
		return hongKongQuote{}, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0")
	req.Header.Set("Referer", "https://gu.qq.com/")
	res, err := client.Do(req)
	if err != nil {
		return hongKongQuote{}, err
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		return hongKongQuote{}, fmt.Errorf("港股报价源状态码 %d", res.StatusCode)
	}
	body, err := io.ReadAll(transform.NewReader(io.LimitReader(res.Body, 64*1024), simplifiedchinese.GBK.NewDecoder()))
	if err != nil {
		return hongKongQuote{}, err
	}
	return parseHongKongTradeQuote(string(body), code)
}

func parseHongKongTradeQuote(body, code string) (hongKongQuote, error) {
	prefix := `v_hk` + code + `="`
	if !strings.HasPrefix(strings.TrimSpace(body), prefix) {
		return hongKongQuote{}, fmt.Errorf("港股报价代码不匹配")
	}
	fields := strings.Split(strings.TrimSuffix(strings.TrimSpace(body)[len(prefix):], `";`), "~")
	if len(fields) <= 75 || fields[2] != code {
		return hongKongQuote{}, fmt.Errorf("港股报价字段不完整或代码不匹配")
	}
	price, err := strconv.ParseFloat(fields[3], 64)
	if err != nil || math.IsNaN(price) || math.IsInf(price, 0) || price < 0 || price > 1_000_000 {
		return hongKongQuote{}, fmt.Errorf("港股报价无效")
	}
	lot, err := strconv.ParseFloat(fields[60], 64)
	if err != nil || math.IsNaN(lot) || math.IsInf(lot, 0) || lot < 1 || lot > 1_000_000 || lot != math.Trunc(lot) {
		return hongKongQuote{}, fmt.Errorf("港股每手股数无效")
	}
	at, err := time.ParseInLocation("2006/01/02 15:04:05", fields[30], time.FixedZone("Asia/Hong_Kong", 8*3600))
	if err != nil {
		return hongKongQuote{}, fmt.Errorf("港股报价时间无效")
	}
	change, err := strconv.ParseFloat(fields[32], 64)
	if err != nil || math.IsNaN(change) || math.IsInf(change, 0) {
		return hongKongQuote{}, fmt.Errorf("港股涨跌幅无效")
	}
	bid, ask := make([]orderBookLevel, 5), make([]orderBookLevel, 5)
	for index := 0; index < 5; index++ {
		bid[index] = parseHongKongDepthLevel(fields[9+index*2], fields[10+index*2])
		ask[index] = parseHongKongDepthLevel(fields[19+index*2], fields[20+index*2])
	}
	return hongKongQuote{Code: code, LastPrice: price, ChangeRate: change, LotSize: lot, Currency: fields[75], Kind: fields[63], QuoteAt: at.Unix(), DepthAt: at.Unix(), Source: "tencent", Bid: bid, Ask: ask}, nil
}

func validateHongKongQuote(quote hongKongQuote, now time.Time) string {
	return validateHongKongQuoteWithCloseTest(quote, now, false)
}

func validateHongKongQuoteWithCloseTest(quote hongKongQuote, now time.Time, closeTest bool) string {
	if math.IsNaN(quote.LastPrice) || math.IsInf(quote.LastPrice, 0) || quote.LastPrice <= 0 || quote.LastPrice > 1_000_000 ||
		math.IsNaN(quote.LotSize) || math.IsInf(quote.LotSize, 0) || quote.LotSize < 1 || quote.LotSize > 1_000_000 || quote.LotSize != math.Trunc(quote.LotSize) {
		return "港股价格或每手股数无效"
	}
	if quote.Currency != "HKD" || quote.Kind != "GP" {
		return "仅支持港币计价普通股票，暂不支持人民币柜台、基金或衍生品"
	}
	if quote.QuoteAt <= 0 || quote.QuoteAt > now.Unix()+60 {
		return "港股报价时间无效或超前，请核对数据源及服务器时间"
	}
	if now.Unix()-quote.QuoteAt > 30*60 && !closeTest {
		return "港股报价超过30分钟；请刷新报价。全天模拟仅允许在盘外使用最近工作日的收市阶段报价，盘中仍需有效新报价"
	}
	return ""
}

func localHongKongTestRequest(c *gin.Context) bool {
	if config.RunModel != config.RunModelDevValue {
		return false
	}
	host, _, err := net.SplitHostPort(c.Request.RemoteAddr)
	if err != nil || !net.ParseIP(host).IsLoopback() || !net.ParseIP(c.ClientIP()).IsLoopback() || c.GetHeader("Forwarded") != "" {
		return false
	}
	for _, header := range []string{"X-Forwarded-For", "X-Real-IP"} {
		if value := c.GetHeader(header); value != "" {
			for _, address := range strings.Split(value, ",") {
				if !net.ParseIP(strings.TrimSpace(address)).IsLoopback() {
					return false
				}
			}
		}
	}
	return true
}

// 本机开发请求与独立开关共同生效，只接受当天收市阶段报价，不跨日模拟。
func hongKongClosePriceTest(quote hongKongQuote, now time.Time, settings mobileTradeSettings, localRequest bool) bool {
	if !localRequest || !settings.HKTrade.LocalCloseTest {
		return false
	}
	location := time.FixedZone("HK", 8*3600)
	current, at := now.In(location), time.Unix(quote.QuoteAt, 0).In(location)
	return current.Weekday() != time.Saturday && current.Weekday() != time.Sunday &&
		current.Hour()*60+current.Minute() >= 16*60+10 &&
		current.Format("2006-01-02") == at.Format("2006-01-02") &&
		at.Hour() >= 16 && quote.QuoteAt <= now.Unix()
}

func hongKongClosePriceSimulation(quote hongKongQuote, now time.Time, settings mobileTradeSettings) bool {
	if !settings.Trade.AllDay || now.Unix()-quote.QuoteAt <= 30*60 {
		return false
	}
	location := time.FixedZone("HK", 8*3600)
	current, at := now.In(location), time.Unix(quote.QuoteAt, 0).In(location)
	minute := current.Hour()*60 + current.Minute()
	weekday := current.Weekday() != time.Saturday && current.Weekday() != time.Sunday
	// 午休及交易时段不回放旧收市价；16:10后才能使用当天收市阶段报价。
	if weekday && minute >= 9*60+30 && minute < 16*60+10 {
		return false
	}
	closeDate := current
	if !weekday || minute < 16*60+10 {
		closeDate = closeDate.AddDate(0, 0, -1)
		for closeDate.Weekday() == time.Saturday || closeDate.Weekday() == time.Sunday {
			closeDate = closeDate.AddDate(0, 0, -1)
		}
	}
	atMinute := at.Hour()*60 + at.Minute()
	return at.Format("2006-01-02") == closeDate.Format("2006-01-02") &&
		atMinute >= 16*60 && atMinute < 16*60+30 && quote.QuoteAt <= now.Unix()
}

func validateHongKongSettings(settings mobileTradeSettings) string {
	if !settings.HKTrade.Enabled {
		return "港股模拟交易未开启，请联系管理员"
	}
	for _, rate := range []float64{settings.HKTrade.Commission, settings.HKTrade.StampDuty, settings.HKTrade.RegulatoryFee, settings.HKTrade.TradingFee, settings.HKTrade.SettlementFee} {
		if math.IsNaN(rate) || math.IsInf(rate, 0) || rate < 0 || rate > 1 {
			return "港股费率配置无效"
		}
	}
	if math.IsNaN(settings.HKTrade.MinCommission) || math.IsInf(settings.HKTrade.MinCommission, 0) || settings.HKTrade.MinCommission < 0 {
		return "港股最低佣金配置无效"
	}
	return ""
}

func hongKongTradeTime(now time.Time, settings mobileTradeSettings) bool {
	return settings.Trade.AllDay || isMobileTradingTime(now, "09:30:00", "12:00:00", "13:00:00", "16:00:00")
}

func HongKongTradeQuote(c *gin.Context) {
	security, ok := findSecurity(c.Param("code"))
	if !ok || !isHongKongSecurity(security) {
		response.ReturnError(c, response.NOT_FOUND, "港股证券不存在")
		return
	}
	quote, err := fetchHongKongTradeQuote(c.Request.Context(), security.Code)
	if err != nil {
		zap.L().Warn("读取港股模拟报价失败", zap.Error(err))
		response.ReturnError(c, response.UNAVAILABLE, "港股报价暂不可用，请重试")
		return
	}
	quote.SyncWarning = stock.PriorityWarning(security.Symbol)
	settings, err := loadMobileTradeSettings()
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取港股交易配置失败")
		return
	}
	customer, ok := currentVerificationCustomer(c)
	if !ok {
		return
	}
	now := time.Now()
	closeTest := hongKongClosePriceTest(quote, now, settings, localHongKongTestRequest(c))
	closeSimulation := hongKongClosePriceSimulation(quote, now, settings)
	reason := validateHongKongSettings(settings)
	if reason == "" && security.Status != system.StatusEnabled {
		reason = "该港股尚未在后台启用交易"
	}
	if reason == "" {
		reason = validateHongKongQuoteWithCloseTest(quote, now, closeTest || closeSimulation)
	}
	if reason == "" && !closeTest && !hongKongTradeTime(now, settings) {
		reason = "当前不在港股模拟交易时段（09:30–12:00、13:00–16:00）"
	}
	limitReason := validateHongKongLimitMetadata(quote, security, settings, now)
	response.ReturnData(c, gin.H{"quote": quote, "tradable": reason == "", "reason": reason, "balance": customer.HKDBalance, "fees": settings.HKTrade, "local_close_test": closeTest, "close_price_simulation": closeSimulation, "limit_order_allowed": limitReason == "", "limit_order_reason": limitReason})
}

type hongKongOrderInput struct {
	orderInput
	RequestID    string `json:"request_id"`
	LimitOrderID uint   `json:"-"`
}

func PlaceHongKongOrder(c *gin.Context) {
	var input hongKongOrderInput
	if err := c.ShouldBindJSON(&input); err != nil || !exchangeRequestPattern.MatchString(input.RequestID) || (input.Direction != "买入" && input.Direction != "卖出") ||
		math.IsNaN(input.Quantity) || math.IsInf(input.Quantity, 0) || input.Quantity < 1 || input.Quantity > 100_000_000 || input.Quantity != math.Trunc(input.Quantity) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "港股请求编号、方向或数量无效")
		return
	}
	input.Code = strings.TrimSuffix(strings.ToUpper(strings.TrimSpace(input.Code)), ".HK")
	// 先查回执：重试不依赖行情源、交易时段或当前证券启用状态。
	var receipt system.HongKongTradeRequest
	customerID := middleware.GetCurrentCustomerID(c)
	err := pgdb.GetClient().Where("customer_id = ? AND request_id = ?", customerID, input.RequestID).First(&receipt).Error
	if err == nil {
		result, err := replayHongKongOrder(receipt, input)
		if err != nil {
			response.ReturnError(c, response.FAILED_PRECONDITION, err.Error())
			return
		}
		response.ReturnData(c, result)
		return
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		response.ReturnError(c, response.DATA_LOSS, "读取成交回执失败，请用原请求重试")
		return
	}
	security, ok := findSecurity(input.Code)
	if !ok || !isHongKongSecurity(security) {
		response.ReturnError(c, response.NOT_FOUND, "港股证券不存在")
		return
	}
	input.Code = security.Code
	quote, err := fetchHongKongTradeQuote(c.Request.Context(), security.Code)
	if err != nil {
		zap.L().Warn("港股模拟成交报价失败", zap.Error(err))
		response.ReturnError(c, response.UNAVAILABLE, "报价不可用，请用原请求重试")
		return
	}
	var result orderResult
	err = pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
		var customer system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, customerID).Error; err != nil {
			return err
		}
		var err error
		result, err = executeHongKongOrder(tx, &customer, security, quote, input, time.Now(), localHongKongTestRequest(c))
		return err
	})
	var rejected exchangeRejection
	if errors.As(err, &rejected) {
		response.ReturnError(c, response.FAILED_PRECONDITION, rejected.Error())
		return
	}
	if err != nil {
		zap.L().Error("港股模拟成交失败", zap.Error(err))
		response.ReturnError(c, response.DATA_LOSS, "交易失败，请用原请求重试并核对资金明细")
		return
	}
	response.ReturnData(c, result)
}

func replayHongKongOrder(receipt system.HongKongTradeRequest, input hongKongOrderInput) (orderResult, error) {
	var result orderResult
	if receipt.Code != input.Code || receipt.Direction != input.Direction || receipt.Quantity != input.Quantity {
		return result, exchangeRejection("请求编号已用于其他港股委托")
	}
	if err := json.Unmarshal([]byte(receipt.Result), &result); err != nil {
		return result, err
	}
	return result, nil
}

// 调用方锁定客户行，确保兑换、买卖和重试共用同一资金锁。
func executeHongKongOrder(tx *gorm.DB, customer *system.Customer, security system.StockSecurity, quote hongKongQuote, input hongKongOrderInput, now time.Time, localRequest bool) (orderResult, error) {
	var result orderResult
	var receipt system.HongKongTradeRequest
	err := tx.Where("customer_id = ? AND request_id = ?", customer.ID, input.RequestID).First(&receipt).Error
	if err == nil {
		return replayHongKongOrder(receipt, input)
	}
	if !errors.Is(err, gorm.ErrRecordNotFound) {
		return result, err
	}
	if customer.Status != system.StatusEnabled || customer.FundStatus != system.StatusEnabled || customer.Verified != system.StatusEnabled {
		return result, exchangeRejection("账户未完成认证或资金账户不可用")
	}
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&security, security.ID).Error; err != nil {
		return result, err
	}
	if !isHongKongSecurity(security) || security.Status != system.StatusEnabled || quote.Code != security.Code {
		return result, exchangeRejection("港股证券不可交易")
	}
	var setting system.AppSystemSetting
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&setting).Error; err != nil {
		return result, err
	}
	settings, err := parseMobileTradeSettings(setting.Config)
	if err != nil {
		return result, err
	}
	if message := validateHongKongSettings(settings); message != "" {
		return result, exchangeRejection(message)
	}
	closeTest := input.LimitOrderID == 0 && hongKongClosePriceTest(quote, now, settings, localRequest)
	closeSimulation := input.LimitOrderID == 0 && hongKongClosePriceSimulation(quote, now, settings)
	if message := validateHongKongQuoteWithCloseTest(quote, now, closeTest || closeSimulation); message != "" {
		return result, exchangeRejection(message)
	}
	if !closeTest && !hongKongTradeTime(now, settings) {
		return result, exchangeRejection("当前不在港股模拟交易时段")
	}
	if input.Direction != "买入" && input.Direction != "卖出" || math.IsNaN(input.Quantity) || math.IsInf(input.Quantity, 0) || input.Quantity <= 0 || input.Quantity > 100_000_000 || input.Quantity != math.Trunc(input.Quantity) {
		return result, exchangeRejection("港股交易数量或方向无效")
	}
	if input.Direction == "买入" && math.Mod(input.Quantity, quote.LotSize) != 0 {
		return result, exchangeRejection(fmt.Sprintf("该港股每手 %.0f 股，买入数量必须为整手", quote.LotSize))
	}
	security.LastPrice = quote.LastPrice
	amount, commission, transferFee, _, stampDuty, totalFee := calculateTradeFees(security, input.Quantity, input.Direction, settings)
	if amount <= 0 || amount > 1_000_000_000 {
		return result, exchangeRejection("成交金额超出模拟交易限制")
	}
	var position system.TradePosition
	found := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id = ? AND symbol = ?", customer.ID, security.Symbol).First(&position).Error
	if found != nil && !errors.Is(found, gorm.ErrRecordNotFound) {
		return result, found
	}
	if found == nil && position.Currency != "HKD" {
		return result, exchangeRejection("持仓币种异常，请联系管理员")
	}
	before := customer.HKDBalance
	if input.Direction == "买入" {
		if customer.HKDBalance < roundMoney(amount+totalFee) {
			return result, exchangeRejection("港币余额不足（成交额及手续费均从港币账户扣除）")
		}
		if errors.Is(found, gorm.ErrRecordNotFound) {
			position = system.TradePosition{CustomerID: customer.ID, Symbol: security.Symbol, StockName: security.Name, Currency: "HKD", BuyAt: now.Unix()}
		}
		customer.HKDBalance = roundMoney(customer.HKDBalance - amount - totalFee)
		position.PositionQty += input.Quantity
		position.AvailableQty += input.Quantity
		position.TotalCost = roundMoney(position.TotalCost + amount)
		position.Margin = position.TotalCost
		position.CostPrice = position.TotalCost / position.PositionQty
		position.Status = system.StatusEnabled
	} else {
		if errors.Is(found, gorm.ErrRecordNotFound) || position.AvailableQty < input.Quantity {
			return result, exchangeRejection("港股可卖持仓不足")
		}
		if input.LimitOrderID == 0 && input.Quantity != position.AvailableQty && math.Mod(input.Quantity, quote.LotSize) != 0 {
			return result, exchangeRejection("非全部卖出时，数量须为整手")
		}
		if roundMoney(customer.HKDBalance+amount-totalFee) < 0 {
			return result, exchangeRejection("港币余额不足以支付卖出费用")
		}
		customer.HKDBalance = roundMoney(customer.HKDBalance + amount - totalFee)
		removed := roundMoney(position.CostPrice * input.Quantity)
		position.PositionQty -= input.Quantity
		position.AvailableQty -= input.Quantity
		position.TotalCost = math.Max(0, roundMoney(position.TotalCost-removed))
		position.Margin = position.TotalCost
		if position.PositionQty == 0 {
			position.TotalCost = 0
			position.Margin = 0
			position.CostPrice = 0
			position.Status = system.StatusDisabled
		}
	}
	position.Leverage = 1
	position.CurrentPrice = quote.LastPrice
	position.MarketValue, position.ProfitLoss, position.ProfitRate = calculatePositionValues(position.PositionQty, quote.LastPrice, position.TotalCost, 1)
	if err := tx.Model(customer).Update("hkd_balance", customer.HKDBalance).Error; err != nil {
		return result, err
	}
	if err := tx.Save(&position).Error; err != nil {
		return result, err
	}
	if err := tx.Model(&system.StockSecurity{}).Where("id = ? AND quote_at <= ?", security.ID, quote.QuoteAt).
		Updates(map[string]any{"last_price": quote.LastPrice, "change_rate": quote.ChangeRate, "quote_at": quote.QuoteAt, "source": quote.Source}).Error; err != nil {
		return result, err
	}
	record := system.TradeRecord{CustomerID: customer.ID, Symbol: security.Symbol, StockName: security.Name, Currency: "HKD", Direction: input.Direction, TradePrice: quote.LastPrice, Quantity: input.Quantity, Amount: amount, Commission: commission, StampDuty: stampDuty, TransferFee: transferFee, Remark: fmt.Sprintf("港股市价模拟成交；来源 %s；报价时间 %s（可能延迟）", quote.Source, time.Unix(quote.QuoteAt, 0).In(time.FixedZone("HK", 8*3600)).Format("2006-01-02 15:04:05")), TradeAt: now.Unix()}
	if input.LimitOrderID > 0 {
		record.Remark = fmt.Sprintf("港股限价委托 %d 按有效买卖盘口模拟成交（非实盘，不模拟排队或部分成交）；来源腾讯；盘口时间 %s", input.LimitOrderID, time.Unix(quote.QuoteAt, 0).In(time.FixedZone("HK", 8*3600)).Format("2006-01-02 15:04:05"))
	}
	if closeTest {
		record.Remark = "本机收市价模拟测试（非实时成交）；" + record.Remark
	}
	if closeSimulation {
		record.Remark = "全天收市价模拟成交（非实时、非官方收市价）；" + record.Remark
	}
	if err := tx.Create(&record).Error; err != nil {
		return result, err
	}
	direction := "入账"
	if customer.HKDBalance < before {
		direction = "扣款"
	}
	flow := system.CustomerFundRecord{CustomerID: customer.ID, Type: "港股模拟交易", Direction: direction, Currency: "HKD", Amount: roundMoney(customer.HKDBalance - before), Balance: customer.HKDBalance, Remark: fmt.Sprintf("%s %s %.0f股；成交额 %.2f，佣金 %.2f，印花税 %.2f，其他费 %.2f；请求 %s", security.Name, input.Direction, input.Quantity, amount, commission, stampDuty, transferFee, input.RequestID)}
	if input.LimitOrderID > 0 {
		flow.Remark = record.Remark + "；" + flow.Remark
	}
	if closeTest || closeSimulation {
		flow.Remark = record.Remark + "；" + flow.Remark
	}
	if err := tx.Create(&flow).Error; err != nil {
		return result, err
	}
	result = orderResult{LocalCloseTest: closeTest, ClosePriceSimulation: closeSimulation, Currency: "HKD", QuoteAt: quote.QuoteAt, RecordID: record.ID, PositionID: position.ID, Direction: input.Direction, Price: quote.LastPrice, Quantity: input.Quantity, Amount: amount, Commission: commission, StampDuty: stampDuty, TransferFee: transferFee, TotalFee: roundMoney(totalFee), BalanceAfter: customer.HKDBalance, ExecutedAt: now.Unix()}
	raw, err := json.Marshal(result)
	if err != nil {
		return result, err
	}
	receipt = system.HongKongTradeRequest{CustomerID: customer.ID, RequestID: input.RequestID, Code: input.Code, Direction: input.Direction, Quantity: input.Quantity, Result: string(raw)}
	return result, tx.Create(&receipt).Error
}
