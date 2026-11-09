package mobile

import (
	"encoding/json"
	"time"

	"github.com/gin-gonic/gin"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

type noticeView struct {
	ID        uint      `json:"id"`
	Title     string    `json:"title"`
	Content   string    `json:"content"`
	Popup     bool      `json:"popup"`
	CreatedAt time.Time `json:"created_at"`
}

func ListCustomerNotices(c *gin.Context) {
	var notices []system.AppNotice
	if err := pgdb.GetClient().
		Where("status = ? AND deleted_at IS NULL", system.StatusEnabled).
		Order("id DESC").
		Find(&notices).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取公告失败")
		return
	}

	customerID := middleware.GetCurrentCustomerID(c)
	result := make([]noticeView, 0, len(notices))
	for _, notice := range notices {
		if !noticeVisibleToCustomer(notice.RecipientIDs, customerID) {
			continue
		}
		result = append(result, noticeView{
			ID: notice.ID, Title: notice.Title, Content: notice.Content, Popup: notice.Popup, CreatedAt: notice.CreatedAt,
		})
	}
	response.ReturnData(c, gin.H{"records": result})
}

func noticeVisibleToCustomer(rawRecipients string, customerID uint) bool {
	var recipients []uint
	if err := json.Unmarshal([]byte(rawRecipients), &recipients); err != nil || len(recipients) == 0 {
		return true
	}
	for _, recipientID := range recipients {
		if recipientID == customerID {
			return true
		}
	}
	return false
}
