package mobile

import (
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"api-server/util/hkquote"
	"api-server/util/positionquote"
)

type positionValuationView struct {
	Holding   tradeHoldingView   `json:"holding"`
	CostPrice float64            `json:"cost_price"`
	Margin    float64            `json:"margin"`
	Valuation *tradePositionView `json:"valuation"`
	Error     string             `json:"error,omitempty"`
}

func availablePositionViews(items []system.TradePosition, quotes map[string]positionquote.Quote) ([]positionValuationView, bool) {
	rows := make([]positionValuationView, 0, len(items))
	complete := true
	for _, item := range items {
		row := positionValuationView{Holding: holdingView(item), CostPrice: item.CostPrice, Margin: item.Margin}
		if row.Margin <= 0 && item.TotalCost > 0 {
			row.Margin = roundMoney(item.TotalCost)
		}
		if quote, ok := quotes[item.Symbol]; ok && quote.Price > 0 && quote.At > 0 {
			valuation := valueCustomerPosition(item, quote)
			row.Valuation = &valuation
		} else {
			complete = false
			row.Error = item.Symbol + " 缺少通过时效检查的报价，估值暂不可用"
		}
		rows = append(rows, row)
	}
	return rows, complete
}

func ListCustomerPositionValuations(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	customerID := middleware.GetCurrentCustomerID(c)
	var items []system.TradePosition
	if err := customerPositionQuery(pgdb.GetClient(), customerID, c.Query("market")).Find(&items).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取持仓失败")
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
