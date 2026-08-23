package mobile

import (
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

func ListWatchlist(c *gin.Context) {
	customerID := middleware.GetCurrentCustomerID(c)
	var items []system.StockSecurity
	err := pgdb.GetClient().Model(&system.StockSecurity{}).
		Select("stock_securities.*").
		Joins("JOIN customer_watchlists ON customer_watchlists.security_id = stock_securities.id AND customer_watchlists.deleted_at IS NULL").
		Where("customer_watchlists.customer_id = ? AND stock_securities.deleted_at IS NULL", customerID).
		Order("customer_watchlists.id DESC").Find(&items).Error
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取自选列表失败")
		return
	}
	records := make([]securityView, len(items))
	for index, item := range items {
		records[index] = toSecurityView(item)
	}
	response.ReturnData(c, records)
}

func WatchlistStatus(c *gin.Context) {
	security, ok := findSecurity(c.Param("code"))
	if !ok {
		response.ReturnError(c, response.NOT_FOUND, "证券不存在")
		return
	}
	var count int64
	if err := pgdb.GetClient().Model(&system.CustomerWatchlist{}).Where("customer_id = ? AND security_id = ?", middleware.GetCurrentCustomerID(c), security.ID).Count(&count).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取自选状态失败")
		return
	}
	response.ReturnData(c, gin.H{"watchlisted": count > 0})
}

func AddWatchlist(c *gin.Context) {
	var input struct {
		Code string `json:"code"`
	}
	if !middleware.CheckParam(&input, c) || strings.TrimSpace(input.Code) == "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "证券代码不能为空")
		return
	}
	security, ok := findSecurity(input.Code)
	if !ok {
		response.ReturnError(c, response.NOT_FOUND, "证券不存在")
		return
	}
	item := system.CustomerWatchlist{CustomerID: middleware.GetCurrentCustomerID(c), SecurityID: security.ID}
	var existing system.CustomerWatchlist
	err := pgdb.GetClient().Unscoped().Where("customer_id = ? AND security_id = ?", item.CustomerID, item.SecurityID).First(&existing).Error
	if err == nil {
		if existing.DeletedAt.Valid {
			if err := pgdb.GetClient().Unscoped().Model(&existing).Updates(map[string]any{"deleted_at": nil}).Error; err != nil {
				response.ReturnError(c, response.DATA_LOSS, "添加自选失败")
				return
			}
		}
		response.ReturnData(c, gin.H{"watchlisted": true})
		return
	}
	if err != gorm.ErrRecordNotFound || pgdb.GetClient().Create(&item).Error != nil {
		response.ReturnError(c, response.DATA_LOSS, "添加自选失败")
		return
	}
	response.ReturnData(c, gin.H{"watchlisted": true})
}

func DeleteWatchlist(c *gin.Context) {
	security, ok := findSecurity(c.Param("code"))
	if !ok {
		response.ReturnError(c, response.NOT_FOUND, "证券不存在")
		return
	}
	if err := pgdb.GetClient().Where("customer_id = ? AND security_id = ?", middleware.GetCurrentCustomerID(c), security.ID).Delete(&system.CustomerWatchlist{}).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "移除自选失败")
		return
	}
	response.ReturnData(c, gin.H{"watchlisted": false})
}

func findSecurity(code string) (system.StockSecurity, bool) {
	var security system.StockSecurity
	code = strings.TrimSpace(code)
	err := pgdb.GetClient().Where("code = ? OR symbol = ?", code, code).First(&security).Error
	return security, err == nil
}
