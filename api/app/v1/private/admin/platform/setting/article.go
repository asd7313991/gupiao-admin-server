package setting

import (
	"github.com/gin-gonic/gin"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

type articleInput struct {
	ID      uint   `json:"id"`
	Title   string `json:"title"`
	Type    string `json:"type"`
	Content string `json:"content"`
	Status  uint   `json:"status"`
}

func ListArticles(c *gin.Context) {
	page, pageSize := middleware.GetPage(c), middleware.GetPageSize(c)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}
	db := pgdb.GetClient().Model(&system.AppArticle{}).Where("deleted_at IS NULL")
	if title := c.Query("title"); title != "" {
		db = db.Where("title LIKE ?", "%"+title+"%")
	}
	if articleType := c.Query("type"); articleType != "" {
		db = db.Where("type = ?", articleType)
	}
	if status := c.Query("status"); status != "" {
		db = db.Where("status = ?", status)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询文章失败")
		return
	}
	var articles []system.AppArticle
	if err := db.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&articles).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询文章失败")
		return
	}
	response.ReturnData(c, gin.H{"records": articles, "total": total})
}

func SaveArticle(c *gin.Context) {
	var input articleInput
	if !middleware.CheckParam(&input, c) || input.Title == "" || input.Type == "" || input.Content == "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "文章标题、类型和内容为必填项")
		return
	}
	if input.Status == 0 {
		input.Status = system.StatusEnabled
	}
	article := system.AppArticle{Title: input.Title, Type: input.Type, Content: input.Content, Status: input.Status}
	if input.ID == 0 {
		if err := pgdb.GetClient().Create(&article).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "添加文章失败")
			return
		}
	} else {
		article.ID = input.ID
		if err := pgdb.GetClient().Save(&article).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "修改文章失败")
			return
		}
	}
	response.ReturnData(c, article)
}

func DeleteArticle(c *gin.Context) {
	var input struct {
		ID uint `json:"id" form:"id"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "文章 ID 无效")
		return
	}
	if err := pgdb.GetClient().Delete(&system.AppArticle{}, input.ID).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "删除文章失败")
		return
	}
	response.ReturnData(c, nil)
}
func UpdateArticleStatus(c *gin.Context) {
	var input struct {
		ID     uint `json:"id" form:"id"`
		Status uint `json:"status" form:"status"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "文章 ID 无效")
		return
	}
	if input.Status != system.StatusEnabled {
		input.Status = system.StatusDisabled
	}
	if err := pgdb.GetClient().Model(&system.AppArticle{}).Where("id = ?", input.ID).Update("status", input.Status).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "更新文章状态失败")
		return
	}
	response.ReturnData(c, nil)
}
