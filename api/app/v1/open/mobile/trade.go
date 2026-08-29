package mobile

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

type mobileTradeSettings struct {
	Trade struct {
		BuyCommission  float64 `json:"buyCommission"`
		SellCommission float64 `json:"sellCommission"`
		MinCommission  float64 `json:"minCommission"`
		StampDuty      float64 `json:"stampDuty"`
		TransferFee    float64 `json:"transferFee"`
		ManagementFee  float64 `json:"managementFee"`
		MorningStart   string  `json:"morningStart"`
		MorningEnd     string  `json:"morningEnd"`
		AfternoonStart string  `json:"afternoonStart"`
		AfternoonEnd   string  `json:"afternoonEnd"`
		AllDay         bool    `json:"allDay"`
	} `json:"trade"`
	Limits struct {
		StarBoard     float64 `json:"starBoard"`
		BeijingBoard  float64 `json:"beijingBoard"`
		MainBoard     float64 `json:"mainBoard"`
		GrowthBoard   float64 `json:"growthBoard"`
		MinStarShares float64 `json:"minStarShares"`
		STTrade       bool    `json:"stTrade"`
		NewStockTrade bool    `json:"newStockTrade"`
	} `json:"limits"`
}

type orderInput struct {
	Code      string  `json:"code"`
	Direction string  `json:"direction"`
	Quantity  float64 `json:"quantity"`
}

type orderResult struct {
	RecordID      uint    `json:"record_id"`
	PositionID    uint    `json:"position_id"`
	Direction     string  `json:"direction"`
	Price         float64 `json:"price"`
	Quantity      float64 `json:"quantity"`
	Amount        float64 `json:"amount"`
	Commission    float64 `json:"commission"`
	StampDuty     float64 `json:"stamp_duty"`
	TransferFee   float64 `json:"transfer_fee"`
	ManagementFee float64 `json:"management_fee"`
	TotalFee      float64 `json:"total_fee"`
	BalanceAfter  float64 `json:"balance_after"`
	ExecutedAt    int64   `json:"executed_at"`
}

type tradePositionView struct {
	ID           uint    `json:"id"`
	Symbol       string  `json:"symbol"`
	StockName    string  `json:"stock_name"`
	PositionQty  float64 `json:"position_qty"`
	AvailableQty float64 `json:"available_qty"`
	CurrentPrice float64 `json:"current_price"`
	CostPrice    float64 `json:"cost_price"`
	MarketValue  float64 `json:"market_value"`
	ProfitLoss   float64 `json:"profit_loss"`
	ProfitRate   float64 `json:"profit_rate"`
}

var errTradeRejected = errors.New("trade rejected")

func ListCustomerPositions(c *gin.Context) {
	customerID := middleware.GetCurrentCustomerID(c)
	var items []system.TradePosition
	if err := pgdb.GetClient().Where("customer_id = ? AND position_qty > 0", customerID).Order("id DESC").Find(&items).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取持仓失败")
		return
	}
	views := make([]tradePositionView, 0, len(items))
	for _, item := range items {
		var security system.StockSecurity
		price := item.CurrentPrice
		if pgdb.GetClient().Where("symbol = ?", item.Symbol).First(&security).Error == nil && security.LastPrice > 0 {
			price = security.LastPrice
		}
		marketValue := item.PositionQty * price
		profitLoss := marketValue - item.TotalCost
		profitRate := float64(0)
		if item.TotalCost > 0 {
			profitRate = profitLoss / item.TotalCost * 100
		}
		views = append(views, tradePositionView{ID: item.ID, Symbol: item.Symbol, StockName: item.StockName, PositionQty: item.PositionQty, AvailableQty: item.AvailableQty, CurrentPrice: price, CostPrice: item.CostPrice, MarketValue: marketValue, ProfitLoss: profitLoss, ProfitRate: profitRate})
	}
	response.ReturnData(c, views)
}

func ListCustomerTradeRecords(c *gin.Context) {
	page, pageSize := middleware.GetPage(c), middleware.GetPageSize(c)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	db := pgdb.GetClient().Model(&system.TradeRecord{}).Where("customer_id = ?", middleware.GetCurrentCustomerID(c))
	var total int64
	if err := db.Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取交易记录失败")
		return
	}
	var records []system.TradeRecord
	if err := db.Order("trade_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&records).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取交易记录失败")
		return
	}
	response.ReturnData(c, gin.H{"records": records, "total": total, "page": page, "page_size": pageSize})
}

func PlaceOrder(c *gin.Context) {
	var input orderInput
	if !middleware.CheckParam(&input, c) {
		return
	}
	input.Code = strings.TrimSpace(input.Code)
	if input.Direction != "买入" && input.Direction != "卖出" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "交易方向无效")
		return
	}
	if input.Quantity <= 0 || input.Quantity != math.Trunc(input.Quantity) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "交易数量必须为正整数")
		return
	}
	settings, err := loadMobileTradeSettings()
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取交易规则失败")
		return
	}
	if !settings.Trade.AllDay && !isMobileTradingTime(time.Now(), settings.Trade.MorningStart, settings.Trade.MorningEnd, settings.Trade.AfternoonStart, settings.Trade.AfternoonEnd) {
		response.ReturnError(c, response.FAILED_PRECONDITION, "当前不在交易时间")
		return
	}
	security, ok := findSecurity(input.Code)
	if !ok || security.Status != system.StatusEnabled || security.LastPrice <= 0 {
		response.ReturnError(c, response.NOT_FOUND, "证券不可交易")
		return
	}
	if message := validateSecurityTrade(security, input, settings); message != "" {
		response.ReturnError(c, response.FAILED_PRECONDITION, message)
		return
	}
	var result orderResult
	var rejection string
	err = pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
		var customer system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, middleware.GetCurrentCustomerID(c)).Error; err != nil {
			return err
		}
		if customer.Status != system.StatusEnabled || customer.FundStatus != system.StatusEnabled {
			rejection = "账户或资金状态不可交易"
			return errTradeRejected
		}
		if customer.Verified != system.StatusEnabled {
			rejection = "请先完成实名认证"
			return errTradeRejected
		}
		return executeMobileOrder(tx, &customer, security, input, settings, &result, &rejection)
	})
	if errors.Is(err, errTradeRejected) {
		response.ReturnError(c, response.FAILED_PRECONDITION, rejection)
		return
	}
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "交易处理失败，请稍后重试")
		return
	}
	response.ReturnData(c, result)
}

func executeMobileOrder(tx *gorm.DB, customer *system.Customer, security system.StockSecurity, input orderInput, settings mobileTradeSettings, result *orderResult, rejection *string) error {
	amount, commission, transferFee, managementFee, stampDuty, totalFee := calculateTradeFees(security.LastPrice, input.Quantity, input.Direction, settings)

	var position system.TradePosition
	positionErr := tx.Clauses(clause.Locking{Strength: "UPDATE"}).Where("customer_id = ? AND symbol = ? AND deleted_at IS NULL", customer.ID, security.Symbol).First(&position).Error
	if input.Direction == "买入" {
		totalDebit := amount + totalFee
		if customer.Balance < totalDebit {
			*rejection = fmt.Sprintf("余额不足，还需 %.2f 元", totalDebit-customer.Balance)
			return errTradeRejected
		}
		customer.Balance = roundMoney(customer.Balance - totalDebit)
		if positionErr == gorm.ErrRecordNotFound {
			position = system.TradePosition{CustomerID: customer.ID, Symbol: security.Symbol, StockName: security.Name, Currency: "CNY", Status: system.StatusEnabled, BuyAt: time.Now().Unix()}
		} else if positionErr != nil {
			return positionErr
		}
		newQty := position.PositionQty + input.Quantity
		position.TotalCost += totalDebit
		position.PositionQty, position.AvailableQty = newQty, position.AvailableQty+input.Quantity
		position.CostPrice = position.TotalCost / newQty
	} else {
		if positionErr != nil || position.AvailableQty < input.Quantity {
			*rejection = "可卖持仓不足"
			return errTradeRejected
		}
		if input.Quantity != position.PositionQty && math.Mod(input.Quantity, 100) != 0 {
			*rejection = "非全部卖出时，数量须为 100 股的整数倍"
			return errTradeRejected
		}
		customer.Balance = roundMoney(customer.Balance + amount - totalFee)
		costRemoved := position.CostPrice * input.Quantity
		realized := amount - totalFee - costRemoved
		if realized >= 0 {
			customer.TotalProfit += realized
		} else {
			customer.TotalLoss += -realized
		}
		position.PositionQty -= input.Quantity
		position.AvailableQty -= input.Quantity
		position.TotalCost = math.Max(0, position.TotalCost-costRemoved)
		if position.PositionQty == 0 {
			position.CostPrice, position.Status = 0, system.StatusDisabled
		}
	}
	position.CurrentPrice = security.LastPrice
	position.MarketValue = position.PositionQty * position.CurrentPrice
	position.ProfitLoss = position.MarketValue - position.TotalCost
	if position.TotalCost > 0 {
		position.ProfitRate = position.ProfitLoss / position.TotalCost * 100
	} else {
		position.ProfitRate = 0
	}
	if err := tx.Save(customer).Error; err != nil {
		return err
	}
	if position.ID == 0 {
		if err := tx.Create(&position).Error; err != nil {
			return err
		}
	} else if err := tx.Save(&position).Error; err != nil {
		return err
	}
	record := system.TradeRecord{CustomerID: customer.ID, Symbol: security.Symbol, StockName: security.Name, Currency: "CNY", Direction: input.Direction, TradePrice: security.LastPrice, Quantity: input.Quantity, Amount: amount, StampDuty: stampDuty, TransferFee: transferFee, Commission: commission + managementFee, Remark: "移动端市价成交", TradeAt: time.Now().Unix()}
	if err := tx.Create(&record).Error; err != nil {
		return err
	}
	*result = orderResult{RecordID: record.ID, PositionID: position.ID, Direction: input.Direction, Price: security.LastPrice, Quantity: input.Quantity, Amount: amount, Commission: commission, StampDuty: stampDuty, TransferFee: transferFee, ManagementFee: managementFee, TotalFee: totalFee, BalanceAfter: customer.Balance, ExecutedAt: record.TradeAt}
	return nil
}

func loadMobileTradeSettings() (mobileTradeSettings, error) {
	var result mobileTradeSettings
	result.Trade.MorningStart, result.Trade.MorningEnd = "09:30:00", "11:30:00"
	result.Trade.AfternoonStart, result.Trade.AfternoonEnd = "13:00:00", "15:00:00"
	result.Limits.MinStarShares = 200
	var row system.AppSystemSetting
	if err := pgdb.GetClient().First(&row).Error; err != nil {
		return result, err
	}
	return result, json.Unmarshal([]byte(row.Config), &result)
}

func validateSecurityTrade(security system.StockSecurity, input orderInput, settings mobileTradeSettings) string {
	if input.Direction == "卖出" {
		return ""
	}
	if strings.HasPrefix(security.Name, "ST") || strings.HasPrefix(security.Name, "*ST") {
		if !settings.Limits.STTrade {
			return "系统暂未开放 ST 股票交易"
		}
	}
	if strings.HasPrefix(security.Name, "N") && !settings.Limits.NewStockTrade {
		return "系统暂未开放新股交易"
	}
	if security.Board == "科创板" {
		if input.Quantity < settings.Limits.MinStarShares {
			return fmt.Sprintf("科创板最低买入 %.0f 股", settings.Limits.MinStarShares)
		}
	} else if math.Mod(input.Quantity, 100) != 0 {
		return "买入数量须为 100 股的整数倍"
	}
	limit := settings.Limits.MainBoard
	if security.Board == "创业板" {
		limit = settings.Limits.GrowthBoard
	}
	if security.Board == "科创板" {
		limit = settings.Limits.StarBoard
	}
	if security.Board == "北交所" {
		limit = settings.Limits.BeijingBoard
	}
	if limit > 0 && security.ChangeRate/100 > limit {
		return "当前涨幅超过系统买入限制"
	}
	return ""
}

func isMobileTradingTime(now time.Time, morningStart, morningEnd, afternoonStart, afternoonEnd string) bool {
	china := time.FixedZone("Asia/Shanghai", 8*60*60)
	current := now.In(china)
	if current.Weekday() == time.Saturday || current.Weekday() == time.Sunday {
		return false
	}
	clock := current.Format("15:04:05")
	return (clock >= morningStart && clock <= morningEnd) || (clock >= afternoonStart && clock <= afternoonEnd)
}

func roundMoney(value float64) float64 { return math.Round(value*100) / 100 }

func calculateTradeFees(price, quantity float64, direction string, settings mobileTradeSettings) (amount, commission, transferFee, managementFee, stampDuty, totalFee float64) {
	amount = roundMoney(price * quantity)
	commissionRate := settings.Trade.BuyCommission
	if direction == "卖出" {
		commissionRate = settings.Trade.SellCommission
	}
	commission = roundMoney(math.Max(amount*commissionRate, settings.Trade.MinCommission))
	transferFee = roundMoney(amount * settings.Trade.TransferFee)
	managementFee = roundMoney(amount * settings.Trade.ManagementFee)
	if direction == "卖出" {
		stampDuty = roundMoney(amount * settings.Trade.StampDuty)
	}
	totalFee = commission + transferFee + managementFee + stampDuty
	return
}
