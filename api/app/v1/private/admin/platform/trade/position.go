package trade

import (
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
	"gorm.io/gorm/clause"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"api-server/util/positionbasis"
)

func ListPositions(c *gin.Context) {
	var items []system.TradePosition
	db := pgdb.GetClient().Where("trade_positions.position_qty > 0").Order("trade_positions.id DESC")
	if phone := c.Query("phone"); phone != "" {
		db = db.Joins("JOIN customers ON customers.id = trade_positions.customer_id").Where("customers.phone LIKE ?", "%"+phone+"%")
	}
	if symbol := c.Query("symbol"); symbol != "" {
		db = db.Where("symbol LIKE ?", "%"+symbol+"%")
	}
	if name := c.Query("stock_name"); name != "" {
		db = db.Where("stock_name LIKE ?", "%"+name+"%")
	}
	if status := c.Query("status"); status != "" {
		db = db.Where("status = ?", status)
	}
	if err := db.Find(&items).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询持仓失败")
		return
	}
	livePrices, err := fetchLivePositionPrices(items)
	if err != nil {
		response.ReturnError(c, response.UNAVAILABLE, "读取实时持仓行情失败，请稍后重试")
		return
	}
	for index := range items {
		if price, exists := livePrices[items[index].Symbol]; exists {
			refreshPositionMarketValues(&items[index], price)
		}
	}
	response.ReturnData(c, items)
}

func fetchLivePositionPrices(positions []system.TradePosition) (map[string]float64, error) {
	symbolToPosition := make(map[string]string)
	symbols := make([]string, 0, len(positions))
	for _, position := range positions {
		symbol := positionQuoteSymbol(position.Symbol)
		if symbol == "" {
			continue
		}
		if _, exists := symbolToPosition[symbol]; !exists {
			symbolToPosition[symbol] = position.Symbol
			symbols = append(symbols, symbol)
		}
	}
	prices := make(map[string]float64)
	client := &http.Client{Timeout: 8 * time.Second}
	for start := 0; start < len(symbols); start += 100 {
		end := start + 100
		if end > len(symbols) {
			end = len(symbols)
		}
		request, err := http.NewRequest(http.MethodGet, "https://qt.gtimg.cn/q="+url.QueryEscape(strings.Join(symbols[start:end], ",")), nil)
		if err != nil {
			return nil, err
		}
		request.Header.Set("User-Agent", "Mozilla/5.0")
		request.Header.Set("Referer", "https://gu.qq.com/")
		result, err := client.Do(request)
		if err != nil {
			return nil, err
		}
		if result.StatusCode != http.StatusOK {
			result.Body.Close()
			return nil, fmt.Errorf("实时行情响应状态异常：%d", result.StatusCode)
		}
		reader := transform.NewReader(result.Body, simplifiedchinese.GBK.NewDecoder())
		body, readErr := io.ReadAll(reader)
		result.Body.Close()
		if readErr != nil {
			return nil, readErr
		}
		for quoteSymbol, price := range parseLivePositionQuotes(string(body)) {
			if positionSymbol, exists := symbolToPosition[quoteSymbol]; exists {
				prices[positionSymbol] = price
			}
		}
	}
	return prices, nil
}

func positionQuoteSymbol(symbol string) string {
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(symbol)), ".")
	if len(parts) != 2 || parts[0] == "" {
		return ""
	}
	switch parts[1] {
	case "SH":
		return "sh" + parts[0]
	case "SZ":
		return "sz" + parts[0]
	case "BJ":
		return "bj" + parts[0]
	case "HK":
		return "hk" + parts[0]
	default:
		return ""
	}
}

func parseLivePositionQuotes(body string) map[string]float64 {
	prices := make(map[string]float64)
	for _, line := range strings.Split(body, ";") {
		separator := strings.Index(line, "=")
		if separator <= 2 {
			continue
		}
		quoteSymbol := strings.TrimPrefix(line[:separator], "v_")
		fields := strings.Split(strings.Trim(line[separator+1:], `"`), "~")
		if len(fields) < 4 {
			continue
		}
		price, err := strconv.ParseFloat(fields[3], 64)
		if err == nil && price > 0 {
			prices[quoteSymbol] = price
		}
	}
	return prices
}

func refreshPositionMarketValues(position *system.TradePosition, price float64) {
	totalCost := position.TotalCost
	if totalCost <= 0 {
		totalCost = position.PositionQty * position.CostPrice
	}
	position.CurrentPrice = price
	valued := *position
	valued.TotalCost = totalCost
	position.MarketValue, position.ProfitLoss, position.ProfitRate = positionbasis.Values(valued, price)
	position.ProfitRate = 0
	if totalCost > 0 {
		position.ProfitRate = roundPositionMoney(position.ProfitLoss / totalCost * 100)
	}
}

func SavePosition(c *gin.Context) {
	var input struct {
		system.TradePosition
		RecordChange bool   `json:"record_change"`
		Phone        string `json:"phone"`
	}
	if !middleware.CheckParam(&input, c) || input.Symbol == "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "证券代码为必填项")
		return
	}
	if input.ID == 0 {
		phone := strings.TrimSpace(input.Phone)
		if phone == "" {
			response.ReturnError(c, response.INVALID_ARGUMENT, "手机号为必填项")
			return
		}

		var customer system.Customer
		if err := pgdb.GetClient().Where("phone = ?", phone).First(&customer).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				response.ReturnError(c, response.NOT_FOUND, "客户不存在")
				return
			}
			response.ReturnError(c, response.DATA_LOSS, "查询客户失败")
			return
		}

		var security system.StockSecurity
		if err := pgdb.GetClient().
			Where("code IN ? OR symbol IN ?", securityLookupValues(input.Symbol), securityLookupValues(input.Symbol)).
			First(&security).Error; err != nil {
			if err == gorm.ErrRecordNotFound {
				response.ReturnError(c, response.NOT_FOUND, "证券不存在")
				return
			}
			response.ReturnError(c, response.DATA_LOSS, "查询证券失败")
			return
		}

		input.CustomerID = customer.ID
		input.Symbol = security.Symbol
		input.StockName = security.Name
		input.Currency = "CNY"
		if strings.HasSuffix(security.Symbol, ".HK") {
			response.ReturnError(c, response.FAILED_PRECONDITION, "港股持仓请通过港币模拟买卖建立，不支持手工新增")
			return
		}
		input.AvailableQty = input.PositionQty
		input.CurrentPrice = security.LastPrice
		if input.CurrentPrice <= 0 {
			input.CurrentPrice = input.CostPrice
		}
		input.Leverage = 1
	}
	if input.BuyAt == 0 {
		input.BuyAt = time.Now().Unix()
	}
	input.TradePosition.TotalCost = input.PositionQty * input.CostPrice
	if input.Leverage < 1 {
		input.Leverage = 5
	}
	if input.ID == 0 {
		input.QuantityBasis = positionbasis.Actual
	} else {
		var existing system.TradePosition
		if err := pgdb.GetClient().First(&existing, input.ID).Error; err != nil {
			response.ReturnError(c, response.NOT_FOUND, "持仓不存在")
			return
		}
		input.QuantityBasis = existing.QuantityBasis
	}
	input.TradePosition.Margin = positionbasis.BuyMargin(input.TotalCost, input.Leverage, input.QuantityBasis)
	input.TradePosition.MarketValue, input.TradePosition.ProfitLoss, input.TradePosition.ProfitRate = positionbasis.Values(input.TradePosition, input.CurrentPrice)
	if input.TradePosition.TotalCost != 0 {
		input.TradePosition.ProfitRate = input.ProfitLoss / input.TotalCost * 100
	}
	if input.Status == 0 {
		input.Status = system.StatusEnabled
	}
	if input.ID == 0 {
		if err := pgdb.GetClient().Create(&input.TradePosition).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "新增持仓失败")
			return
		}
	} else {
		var existing system.TradePosition
		if err := pgdb.GetClient().First(&existing, input.ID).Error; err != nil {
			response.ReturnError(c, response.NOT_FOUND, "持仓不存在")
			return
		}
		input.TradePosition.CreatedAt = existing.CreatedAt
		if strings.HasSuffix(existing.Symbol, ".HK") || strings.HasSuffix(input.Symbol, ".HK") {
			response.ReturnError(c, response.FAILED_PRECONDITION, "港股持仓请通过模拟买卖调整，不能直接覆盖账务")
			return
		}
		if err := pgdb.GetClient().Save(&input.TradePosition).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "修改持仓失败")
			return
		}
	}
	response.ReturnData(c, input.TradePosition)
}

func securityLookupValues(value string) []string {
	normalized := strings.ToUpper(strings.TrimSpace(value))
	if normalized == "" {
		return nil
	}

	values := []string{normalized}
	if len(normalized) == 8 && (strings.HasPrefix(normalized, "SH") || strings.HasPrefix(normalized, "SZ")) {
		values = append(values, normalized[2:]+"."+normalized[:2])
	}
	return values
}

func DeletePosition(c *gin.Context) {
	var input struct {
		ID uint `json:"id"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "持仓 ID 无效")
		return
	}
	var position system.TradePosition
	if err := pgdb.GetClient().First(&position, input.ID).Error; err != nil {
		response.ReturnError(c, response.NOT_FOUND, "持仓不存在")
		return
	}
	if strings.HasSuffix(position.Symbol, ".HK") {
		response.ReturnError(c, response.FAILED_PRECONDITION, "港股请通过卖出或强平结算，不支持直接删除")
		return
	}
	if err := pgdb.GetClient().Delete(&system.TradePosition{}, input.ID).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "删除持仓失败")
		return
	}
	response.ReturnData(c, nil)
}

func ForceClosePosition(c *gin.Context) {
	var input struct {
		ID uint `json:"id"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "持仓 ID 无效")
		return
	}

	var result struct {
		ID           uint    `json:"id"`
		CustomerID   uint    `json:"customer_id"`
		Symbol       string  `json:"symbol"`
		Price        float64 `json:"price"`
		Quantity     float64 `json:"quantity"`
		Amount       float64 `json:"amount"`
		Realized     float64 `json:"realized"`
		BalanceAfter float64 `json:"balance_after"`
	}
	var rejection string
	err := pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
		var snapshot system.TradePosition
		if err := tx.Select("id", "customer_id").First(&snapshot, input.ID).Error; err != nil {
			return err
		}
		var customer system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, snapshot.CustomerID).Error; err != nil {
			return err
		}
		var position system.TradePosition
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&position, input.ID).Error; err != nil {
			return err
		}
		if position.PositionQty <= 0 || position.Status != system.StatusEnabled {
			rejection = "该持仓已关闭，无需重复强平"
			return nil
		}
		var security system.StockSecurity
		if err := tx.Where("symbol = ?", position.Symbol).First(&security).Error; err != nil {
			return err
		}
		if security.LastPrice <= 0 {
			rejection = "证券当前价格无效，无法强平"
			return nil
		}

		price := security.LastPrice
		quantity := position.PositionQty
		amount, realized, settlement := calculateForceCloseSettlement(position, price)
		hongKong := strings.HasSuffix(position.Symbol, ".HK")
		balance := &customer.Balance
		if hongKong {
			if position.Currency != "HKD" || position.Leverage != 1 {
				return fmt.Errorf("港股持仓币种或杠杆异常")
			}
			balance = &customer.HKDBalance
		}
		*balance = roundPositionMoney(*balance + settlement)
		if !hongKong && realized >= 0 {
			customer.TotalProfit = roundPositionMoney(customer.TotalProfit + realized)
		} else if !hongKong {
			customer.TotalLoss = roundPositionMoney(customer.TotalLoss - realized)
		}
		closedAt := time.Now().Unix()
		if err := tx.Model(&system.LimitOrder{}).
			Where("customer_id = ? AND symbol = ? AND direction = ? AND status = ? AND deleted_at IS NULL", customer.ID, position.Symbol, "卖出", "pending").
			Updates(map[string]any{"status": "cancelled", "cancelled_at": closedAt, "frozen_quantity": 0}).Error; err != nil {
			return err
		}
		record := system.TradeRecord{QuantityBasis: position.QuantityBasis, Leverage: positionbasis.Leverage(position), CustomerID: customer.ID, Symbol: position.Symbol, StockName: position.StockName, Currency: position.Currency, Direction: "卖出", TradePrice: price, Quantity: quantity, Amount: amount, Remark: "后台按当前价格强平", TradeAt: closedAt}
		cost := position.TotalCost
		record.RealizedProfit, record.RealizedCost = &realized, &cost
		if err := tx.Create(&record).Error; err != nil {
			return err
		}
		fundDirection := "入账"
		fundAmount := settlement
		if fundAmount < 0 {
			fundDirection = "出账"
			fundAmount = -fundAmount
		}
		if err := tx.Create(&system.CustomerFundRecord{CustomerID: customer.ID, Type: "后台强平", Direction: fundDirection, Currency: position.Currency, Amount: roundPositionMoney(fundAmount), Balance: *balance, Remark: fmt.Sprintf("%s %s 按当前价 %.4f 强平 %.0f 股", position.StockName, position.Symbol, price, quantity)}).Error; err != nil {
			return err
		}
		position.PositionQty, position.AvailableQty = 0, 0
		position.TotalCost, position.Margin, position.MarketValue = 0, 0, 0
		position.CostPrice, position.ProfitLoss, position.ProfitRate = 0, 0, 0
		position.CurrentPrice, position.Status = price, system.StatusDisabled
		if err := tx.Save(&customer).Error; err != nil {
			return err
		}
		if err := tx.Save(&position).Error; err != nil {
			return err
		}
		result.ID, result.CustomerID, result.Symbol = position.ID, customer.ID, position.Symbol
		result.Price, result.Quantity, result.Amount = price, quantity, amount
		result.Realized, result.BalanceAfter = realized, *balance
		return nil
	})
	if rejection != "" {
		response.ReturnError(c, response.FAILED_PRECONDITION, rejection)
		return
	}
	if err == gorm.ErrRecordNotFound {
		response.ReturnError(c, response.NOT_FOUND, "持仓、客户或证券不存在")
		return
	}
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "强平失败，请稍后重试")
		return
	}
	response.ReturnData(c, result)
}

func calculateForceCloseSettlement(position system.TradePosition, price float64) (amount, realized, settlement float64) {
	amount = roundPositionMoney(price * position.PositionQty)
	realized = roundPositionMoney((amount - position.TotalCost) * positionbasis.Multiplier(position))
	settlement = roundPositionMoney(position.Margin + realized)
	return amount, realized, settlement
}

func roundPositionMoney(value float64) float64 {
	return math.Round(value*100) / 100
}
