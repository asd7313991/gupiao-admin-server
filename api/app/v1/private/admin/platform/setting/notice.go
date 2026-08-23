package setting

import (
	"encoding/json"

	"github.com/gin-gonic/gin"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

type noticeInput struct {
	ID           uint   `json:"id"`
	Title        string `json:"title"`
	Content      string `json:"content"`
	Popup        bool   `json:"popup"`
	RecipientIDs []uint `json:"recipient_ids"`
	Status       uint   `json:"status"`
}

func ListNotices(c *gin.Context) {
	page, pageSize := middleware.GetPage(c), middleware.GetPageSize(c)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	db := pgdb.GetClient().Model(&system.AppNotice{}).Where("deleted_at IS NULL")
	if title := c.Query("title"); title != "" {
		db = db.Where("title LIKE ?", "%"+title+"%")
	}
	if popup := c.Query("popup"); popup != "" {
		db = db.Where("popup = ?", popup == "true")
	}
	if status := c.Query("status"); status != "" {
		db = db.Where("status = ?", status)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询公告失败")
		return
	}
	var notices []system.AppNotice
	if err := db.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&notices).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询公告失败")
		return
	}
	response.ReturnData(c, gin.H{"records": notices, "total": total})
}

func SaveNotice(c *gin.Context) {
	var input noticeInput
	if !middleware.CheckParam(&input, c) || input.Title == "" || input.Content == "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "公告标题和内容为必填项")
		return
	}
	recipients, _ := json.Marshal(input.RecipientIDs)
	if input.Status == 0 {
		input.Status = system.StatusEnabled
	}
	notice := system.AppNotice{Title: input.Title, Content: input.Content, Popup: input.Popup, RecipientIDs: string(recipients), Status: input.Status}
	if input.ID == 0 {
		if err := pgdb.GetClient().Create(&notice).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "添加公告失败")
			return
		}
	} else {
		notice.ID = input.ID
		if err := pgdb.GetClient().Save(&notice).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "修改公告失败")
			return
		}
	}
	response.ReturnData(c, notice)
}

func DeleteNotice(c *gin.Context) {
	var input struct {
		ID uint `json:"id"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "公告 ID 无效")
		return
	}
	if err := pgdb.GetClient().Delete(&system.AppNotice{}, input.ID).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "删除公告失败")
		return
	}
	response.ReturnData(c, nil)
}
func UpdateNoticeStatus(c *gin.Context) {
	var input struct {
		ID     uint `json:"id"`
		Status uint `json:"status"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "公告 ID 无效")
		return
	}
	if input.Status != system.StatusEnabled {
		input.Status = system.StatusDisabled
	}
	if err := pgdb.GetClient().Model(&system.AppNotice{}).Where("id = ?", input.ID).Update("status", input.Status).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "更新公告状态失败")
		return
	}
	response.ReturnData(c, nil)
}
