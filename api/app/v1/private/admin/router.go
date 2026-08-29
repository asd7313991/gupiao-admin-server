package admin

import (
	"github.com/gin-gonic/gin"

	"api-server/api/app/v1/private/admin/platform/customer"
	platformFinance "api-server/api/app/v1/private/admin/platform/finance"
	platformMenu "api-server/api/app/v1/private/admin/platform/menu"
	platformNews "api-server/api/app/v1/private/admin/platform/news"
	platformRole "api-server/api/app/v1/private/admin/platform/role"
	platformSetting "api-server/api/app/v1/private/admin/platform/setting"
	platformStock "api-server/api/app/v1/private/admin/platform/stock"
	platformTrade "api-server/api/app/v1/private/admin/platform/trade"
	"api-server/api/app/v1/private/admin/system/department"
	"api-server/api/app/v1/private/admin/system/menu"
	"api-server/api/app/v1/private/admin/system/role"
	"api-server/api/app/v1/private/admin/system/tenant"
	"api-server/api/app/v1/private/admin/system/user"
	"api-server/api/middleware"
)

// RegisterRoutes 在 /api/v1/private/admin 下注册系统管理接口
func RegisterRoutes(admin *gin.RouterGroup) {
	if admin == nil {
		return
	}

	registerSystemRoutes(admin.Group("/system"))
	registerPlatformRoutes(admin.Group("/platform"))
}

func registerSystemRoutes(group *gin.RouterGroup) {
	if group == nil {
		return
	}

	group.GET("/user/login/captcha", middleware.LoginRateLimitMiddleware(), user.GetCaptcha)
	group.POST("/user/login", middleware.LoginRateLimitMiddleware(), user.Login)
	group.GET("/user/login/tenant", middleware.LoginRateLimitMiddleware(), user.SearchTenantCodeForLogin)
	group.GET("/login/log", middleware.TokenVerify, user.FindLoginLogList)
	group.GET("/user/info", middleware.TokenVerify, user.GetUserInfo)
	group.PUT("/user/info", middleware.TokenVerify, user.UpdateUserInfo)
	group.GET("/user/menu", middleware.TokenVerify, user.GetUserMenuList)
	group.GET("/menu/role", middleware.TokenVerify, menu.GetMenuListByRoleID)
	group.PUT("/menu/role", middleware.TokenVerify, menu.UpdateMenuListByRoleID)
	group.GET("/department", middleware.TokenVerify, department.GetDepartmentList)
	group.POST("/department", middleware.TokenVerify, department.AddDepartment)
	group.PUT("/department", middleware.TokenVerify, department.UpdateDepartment)
	group.DELETE("/department", middleware.TokenVerify, department.DeleteDepartment)
	group.GET("/role", middleware.TokenVerify, role.GetRoleList)
	group.POST("/role", middleware.TokenVerify, role.AddRole)
	group.PUT("/role", middleware.TokenVerify, role.UpdateRole)
	group.DELETE("/role", middleware.TokenVerify, role.DeleteRole)
	group.GET("/user", middleware.TokenVerify, user.FindUser)
	group.GET("/user/cache", middleware.TokenVerify, user.FindUserByCache)
	group.POST("/user", middleware.TokenVerify, user.AddUser)
	group.PUT("/user", middleware.TokenVerify, user.UpdateUser)
	group.DELETE("/user", middleware.TokenVerify, user.DeleteUser)
	group.PUT("/user/admin-password", middleware.TokenVerify, middleware.SuperAdminVerify, user.UpdateAdministratorPassword)
	group.PUT("/user/google-auth-secret/reset", middleware.TokenVerify, middleware.SuperAdminVerify, user.ResetAdministratorGoogleAuthSecret)
	group.GET("/tenant", middleware.TokenVerify, middleware.SuperAdminVerify, tenant.FindTenant)
	group.POST("/tenant", middleware.TokenVerify, middleware.SuperAdminVerify, tenant.AddTenant)
	group.PUT("/tenant", middleware.TokenVerify, middleware.SuperAdminVerify, tenant.UpdateTenant)
	group.DELETE("/tenant", middleware.TokenVerify, middleware.SuperAdminVerify, tenant.DeleteTenant)
}

func registerPlatformRoutes(group *gin.RouterGroup) {
	if group == nil {
		return
	}

	menuGroup := group.Group("/menu", middleware.PlatformMenuAccess)
	menuGroup.GET("", platformMenu.GetMenuList)
	menuGroup.POST("", platformMenu.AddMenu)
	menuGroup.PUT("", platformMenu.UpdateMenu)
	menuGroup.DELETE("", platformMenu.DeleteMenu)
	menuGroup.GET("/tenant", platformMenu.GetTenantMenu)
	menuGroup.PUT("/tenant", platformMenu.UpdateTenantMenu)
	menuGroup.GET("/auth", platformMenu.GetMenuAuthList)
	menuGroup.POST("/auth", platformMenu.AddMenuAuth)
	menuGroup.PUT("/auth", platformMenu.UpdateMenuAuth)
	menuGroup.DELETE("/auth", platformMenu.DeleteMenuAuth)

	customerGroup := group.Group("/customer", middleware.PlatformMenuAccess)
	customerGroup.GET("", customer.List)
	customerGroup.GET("/detail", customer.Detail)
	customerGroup.POST("", customer.Create)
	customerGroup.PUT("", customer.Update)
	customerGroup.DELETE("", customer.Delete)
	customerGroup.POST("/deposit", customer.Deposit)
	customerGroup.PUT("/status", customer.SetStatus)
	customerGroup.PUT("/fund-status", customer.SetFundStatus)
	customerGroup.PUT("/password", customer.UpdatePassword)
	customerGroup.PUT("/bank", customer.UpdateBank)
	customerGroup.PUT("/verification/review", customer.ReviewVerification)
	customerGroup.PUT("/verification/review/batch", customer.BatchReviewVerification)
	customerGroup.PUT("/status/batch", customer.BatchSetStatus)
	customerGroup.GET("/fund-records", customer.FundRecords)
	customerGroup.GET("/devices", customer.Devices)
	customerGroup.PUT("/devices/block", customer.SetDeviceBlocked)
	customerGroup.PUT("/devices/block/batch", customer.BatchSetDeviceBlocked)

	tradeGroup := group.Group("/trade", middleware.PlatformMenuAccess)
	tradeGroup.GET("/positions", platformTrade.ListPositions)
	tradeGroup.POST("/positions", platformTrade.SavePosition)
	tradeGroup.PUT("/positions", platformTrade.SavePosition)
	tradeGroup.DELETE("/positions", platformTrade.DeletePosition)
	tradeGroup.GET("/records", platformTrade.ListRecords)
	tradeGroup.POST("/records", platformTrade.SaveRecord)
	tradeGroup.PUT("/records", platformTrade.SaveRecord)
	tradeGroup.DELETE("/records", platformTrade.DeleteRecord)

	settingGroup := group.Group("/setting", middleware.PlatformMenuAccess)
	settingGroup.GET("/system", platformSetting.GetSystemSetting)
	settingGroup.PUT("/system", platformSetting.SaveSystemSetting)
	settingGroup.POST("/system/logo", platformSetting.UploadBrandLogo)
	settingGroup.GET("/notices", platformSetting.ListNotices)
	settingGroup.POST("/notices", platformSetting.SaveNotice)
	settingGroup.PUT("/notices", platformSetting.SaveNotice)
	settingGroup.DELETE("/notices", platformSetting.DeleteNotice)
	settingGroup.PUT("/notices/status", platformSetting.UpdateNoticeStatus)
	settingGroup.GET("/articles", platformSetting.ListArticles)
	settingGroup.POST("/articles", platformSetting.SaveArticle)
	settingGroup.PUT("/articles", platformSetting.SaveArticle)
	settingGroup.DELETE("/articles", platformSetting.DeleteArticle)
	settingGroup.PUT("/articles/status", platformSetting.UpdateArticleStatus)

	newsGroup := group.Group("/news", middleware.PlatformMenuAccess)
	newsGroup.GET("", platformNews.ListNews)
	newsGroup.GET("/:id", platformNews.GetNews)
	newsGroup.PUT("", platformNews.UpdateNews)
	newsGroup.DELETE("", platformNews.DeleteNews)
	newsGroup.POST("/batch-action", platformNews.BatchAction)
	newsGroup.GET("/sources", platformNews.ListSources)
	newsGroup.POST("/sources", platformNews.CreateSource)
	newsGroup.PUT("/sources", platformNews.UpdateSource)
	newsGroup.DELETE("/sources", platformNews.DeleteSource)
	newsGroup.POST("/collect", platformNews.CollectNews)
	newsGroup.GET("/collect-logs", platformNews.ListCollectLogs)

	stockGroup := group.Group("/stock", middleware.PlatformMenuAccess)
	stockGroup.GET("/securities", platformStock.List)
	stockGroup.GET("/securities/exchanges", platformStock.ListExchanges)
	stockGroup.GET("/securities/boards", platformStock.ListBoards)
	stockGroup.POST("/securities", platformStock.Save)
	stockGroup.PUT("/securities", platformStock.Save)
	stockGroup.DELETE("/securities", platformStock.Delete)
	stockGroup.PUT("/securities/status", platformStock.UpdateStatus)
	stockGroup.POST("/securities/sync/eastmoney", platformStock.SyncEastmoney)

	financeGroup := group.Group("/finance", middleware.PlatformMenuAccess)
	financeGroup.GET("/recharges", platformFinance.ListRecharges)
	financeGroup.POST("/recharges", platformFinance.SaveRecharge)
	financeGroup.PUT("/recharges", platformFinance.SaveRecharge)
	financeGroup.GET("/withdrawals", platformFinance.ListWithdrawals)
	financeGroup.POST("/withdrawals", platformFinance.SaveWithdrawal)
	financeGroup.PUT("/withdrawals", platformFinance.SaveWithdrawal)

	secureGroup := group.Group("", middleware.TokenVerify, middleware.SuperAdminVerify)
	secureGroup.GET("/role", platformRole.GetRoleList)
	secureGroup.POST("/role", platformRole.AddRole)
	secureGroup.PUT("/role", platformRole.UpdateRole)
	secureGroup.DELETE("/role", platformRole.DeleteRole)
	secureGroup.GET("/tenant", tenant.FindTenant)
	secureGroup.POST("/tenant", tenant.AddTenant)
	secureGroup.PUT("/tenant", tenant.UpdateTenant)
	secureGroup.DELETE("/tenant", tenant.DeleteTenant)
}
