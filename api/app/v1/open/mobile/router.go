package mobile

import (
	"github.com/gin-gonic/gin"

	"api-server/api/middleware"
)

func RegisterRoutes(open *gin.RouterGroup) {
	group := open.Group("/mobile")
	group.POST("/auth/register", Register)
	group.POST("/auth/login", Login)
	group.GET("/article", MobileArticle)
	group.GET("/profile", middleware.CustomerTokenVerify, Profile)
	group.GET("/indices", ListMarketIndices)
	group.GET("/securities", ListSecurities)
	group.GET("/securities/:code", SecurityDetail)
	group.GET("/securities/:code/klines", SecurityKLines)
	group.GET("/securities/:code/orderbook", SecurityOrderBook)
	account := group.Group("", middleware.CustomerTokenVerify)
	account.GET("/watchlist", ListWatchlist)
	account.GET("/watchlist/:code", WatchlistStatus)
	account.POST("/watchlist", AddWatchlist)
	account.DELETE("/watchlist/:code", DeleteWatchlist)
	account.GET("/trade/positions", ListCustomerPositions)
	account.GET("/trade/records", ListCustomerTradeRecords)
	account.POST("/trade/orders", PlaceOrder)
	account.POST("/trade/limit-orders", PlaceLimitOrder)
	account.GET("/trade/limit-orders", ListLimitOrders)
	account.DELETE("/trade/limit-orders/:id", CancelLimitOrder)
	account.PUT("/profile/login-password", UpdateLoginPassword)
	account.GET("/verification/status", VerificationStatus)
	account.POST("/verification/material", UploadVerificationMaterial)
	account.PUT("/verification/profile", SaveVerificationProfile)
	account.PUT("/verification/bank-card", UpdateVerificationBankCard)
	account.PUT("/verification/trade-password", UpdateVerificationTradePassword)
	account.POST("/verification/face/start", StartFaceVerification)
	account.POST("/verification/face/confirm", ConfirmFaceVerification)
	account.GET("/finance/summary", FinanceSummary)
	account.POST("/finance/recharges", RequestRecharge)
	account.GET("/finance/recharges", ListCustomerRecharges)
	account.POST("/finance/withdrawals", RequestWithdrawal)
	account.GET("/finance/withdrawals", ListCustomerWithdrawals)
}
