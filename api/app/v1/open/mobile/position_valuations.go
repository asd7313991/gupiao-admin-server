package mobile

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"api-server/util/hkquote"
	"api-server/util/positionbasis"
	"api-server/util/positionquote"
)

type positionValuationView struct {
	Holding            tradeHoldingView   `json:"holding"`
	CostPrice          float64            `json:"cost_price"`
	Margin             float64            `json:"margin"`
	BaseMargin         float64            `json:"base_margin"`
	SupplementalMargin float64            `json:"supplemental_margin"`
	Valuation          *tradePositionView `json:"valuation"`
	Error              string             `json:"error,omitempty"`
}

func availablePositionViews(items []system.TradePosition, quotes map[string]positionquote.Quote) ([]positionValuationView, bool) {
	rows := make([]positionValuationView, 0, len(items))
	complete := true
	for _, item := range items {
		row := positionValuationView{Holding: holdingView(item), CostPrice: item.CostPrice, Margin: item.Margin}
		if row.Margin <= 0 && item.TotalCost > 0 {
			basis := item.QuantityBasis
			if positionbasis.IsActual(item) {
				basis = positionbasis.Actual
			}
			row.Margin = positionbasis.BuyMargin(item.TotalCost, positionLeverage(item), basis)
		}
		item.Margin = row.Margin
		row.BaseMargin, row.SupplementalMargin = positionbasis.MarginParts(item)
		if quote, ok := quotes[item.Symbol]; ok && quote.Price > 0 && quote.At > 0 {
			valuation := valueCustomerPosition(item, quote)
			row.Valuation = &valuation
		} else {
			complete = false
			row.Error = item.Symbol + " 暂时没有可用行情，持仓估值先不显示"
		}
		rows = append(rows, row)
	}
	return aggregatePositionValuationViews(rows), complete
}

func positionViewKey(symbol, currency string, id uint) string {
	key := symbol + "\x00" + currency
	if strings.HasSuffix(symbol, ".HK") {
		key += "\x00" + strconv.FormatUint(uint64(id), 10)
	}
	return key
}

func aggregateHoldingViews(items []tradeHoldingView) []tradeHoldingView {
	rows := make([]tradeHoldingView, 0, len(items))
	indexes := make(map[string]int, len(items))
	for _, item := range items {
		key := positionViewKey(item.Symbol, item.Currency, item.ID)
		index, exists := indexes[key]
		if !exists {
			indexes[key] = len(rows)
			rows = append(rows, item)
			continue
		}
		current := &rows[index]
		totalQty := current.PositionQty + item.PositionQty
		if totalQty > 0 {
			current.Leverage = (current.Leverage*current.PositionQty + item.Leverage*item.PositionQty) / totalQty
		}
		current.PositionQty = totalQty
		current.AvailableQty += item.AvailableQty
		if current.QuantityBasis != item.QuantityBasis {
			current.QuantityBasis = 0
		}
		current.SettlementNotice = combineNotices(current.SettlementNotice, item.SettlementNotice)
	}
	return rows
}

func aggregateTradePositionViews(items []tradePositionView) []tradePositionView {
	rows := make([]tradePositionView, 0, len(items))
	indexes := make(map[string]int, len(items))
	for _, item := range items {
		key := positionViewKey(item.Symbol, item.Currency, item.ID)
		index, exists := indexes[key]
		if !exists {
			indexes[key] = len(rows)
			rows = append(rows, item)
			continue
		}
		current := &rows[index]
		totalQty := current.PositionQty + item.PositionQty
		if totalQty > 0 {
			current.Leverage = (current.Leverage*current.PositionQty + item.Leverage*item.PositionQty) / totalQty
			current.CostPrice = (current.TotalCost + item.TotalCost) / totalQty
		}
		current.PositionQty = totalQty
		current.AvailableQty += item.AvailableQty
		current.TotalCost += item.TotalCost
		current.Margin += item.Margin
		current.BaseMargin += item.BaseMargin
		current.SupplementalMargin += item.SupplementalMargin
		current.MarketValue += item.MarketValue
		current.ProfitLoss += item.ProfitLoss
		if current.TotalCost > 0 {
			current.ProfitRate = current.ProfitLoss / current.TotalCost * 100
		}
		if current.QuantityBasis != item.QuantityBasis {
			current.QuantityBasis = 0
		}
		current.SettlementNotice = combineNotices(current.SettlementNotice, item.SettlementNotice)
	}
	return rows
}

func aggregatePositionValuationViews(items []positionValuationView) []positionValuationView {
	rows := make([]positionValuationView, 0, len(items))
	indexes := make(map[string]int, len(items))
	for _, item := range items {
		key := positionViewKey(item.Holding.Symbol, item.Holding.Currency, item.Holding.ID)
		index, exists := indexes[key]
		if !exists {
			indexes[key] = len(rows)
			rows = append(rows, item)
			continue
		}
		current := &rows[index]
		totalQty := current.Holding.PositionQty + item.Holding.PositionQty
		combinedCost := current.CostPrice*current.Holding.PositionQty + item.CostPrice*item.Holding.PositionQty
		if totalQty > 0 {
			current.CostPrice = combinedCost / totalQty
		}
		current.Holding = aggregateHoldingViews([]tradeHoldingView{current.Holding, item.Holding})[0]
		current.Margin += item.Margin
		current.BaseMargin += item.BaseMargin
		current.SupplementalMargin += item.SupplementalMargin
		if current.Valuation == nil || item.Valuation == nil {
			current.Valuation = nil
			current.Error = combineNotices(current.Error, item.Error)
			continue
		}
		current.Valuation = &aggregateTradePositionViews([]tradePositionView{*current.Valuation, *item.Valuation})[0]
	}
	return rows
}

func combineNotices(current, next string) string {
	if next == "" || strings.Contains(current, next) {
		return current
	}
	if current == "" {
		return next
	}
	return current + "；" + next
}

func ListCustomerPositionValuations(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	customerID := middleware.GetCurrentCustomerID(c)
	var items []system.TradePosition
	if err := customerPositionQuery(pgdb.GetClient(), customerID, c.Query("market")).Find(&items).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取持仓失败")
		return
	}
	if err := reconcilePositionViews(pgdb.GetClient(), items, time.Now()); err != nil {
		zap.L().Error("核对持仓成交记录失败", zap.Uint("customer_id", customerID), zap.Error(err))
		response.ReturnError(c, response.DATA_LOSS, "读取持仓交收记录失败")
		return
	}
	symbols := make([]string, 0, len(items))
	for _, item := range items {
		symbols = append(symbols, item.Symbol)
	}
	quotes, err := positionquote.FetchAvailable(c.Request.Context(), &http.Client{Timeout: 8 * time.Second},
		"https://qt.gtimg.cn/q=", hkquote.EastmoneyURL, symbols, time.Now())
	if c.Request.Context().Err() != nil {
		response.ReturnError(c, response.UNAVAILABLE, "持仓估值请求已中断")
		return
	}
	if err != nil {
		zap.L().Warn("部分持仓估值不可用，保留基础持仓并明确标记缺失估值", zap.Uint("customer_id", customerID), zap.Error(err))
	}
	rows, complete := availablePositionViews(items, quotes)
	response.ReturnData(c, gin.H{"positions": rows, "complete": complete})
}
