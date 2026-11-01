package setting

import (
	"crypto/rand"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"api-server/util/articleimage"
)

type articleInput struct {
	ID           uint      `json:"id"`
	Title        string    `json:"title"`
	Type         string    `json:"type"`
	Content      string    `json:"content"`
	Status       uint      `json:"status"`
	FooterImages *[]string `json:"footer_images"`
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
	var article system.AppArticle
	if input.ID != 0 {
		if err := pgdb.GetClient().First(&article, input.ID).Error; err != nil {
			if errors.Is(err, gorm.ErrRecordNotFound) {
				response.ReturnError(c, response.NOT_FOUND, "文章不存在")
			} else {
				response.ReturnError(c, response.DATA_LOSS, "读取文章失败")
			}
			return
		}
	}
	if input.FooterImages != nil {
		if err := articleimage.CheckReferences(*input.FooterImages); err != nil {
			response.ReturnError(c, response.INVALID_ARGUMENT, err.Error())
			return
		}
		article.FooterImages = *input.FooterImages
	}
	if input.Type != "法律声明" && len(article.FooterImages) > 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "底部照片仅用于法律声明，修改类型前请移除照片")
		return
	}
	if article.FooterImages == nil {
		article.FooterImages = []string{}
	}
	article.Title, article.Type, article.Content, article.Status = input.Title, input.Type, input.Content, input.Status
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

func UploadArticleImage(c *gin.Context) {
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, articleimage.MaxBytes+1024*1024)
	file, err := c.FormFile("file")
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	if err != nil || file.Size <= 0 || file.Size > articleimage.MaxBytes {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请选择不超过 5MB 的照片")
		return
	}
	source, err := file.Open()
	if err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "读取照片失败")
		return
	}
	defer source.Close()
	data, err := io.ReadAll(io.LimitReader(source, articleimage.MaxBytes+1))
	if err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "读取照片失败")
		return
	}
	extension, err := articleimage.Check(data)
	if err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, err.Error())
		return
	}
	if err := os.MkdirAll(articleimage.Directory, 0o755); err != nil {
		response.ReturnError(c, response.DATA_LOSS, "创建照片目录失败")
		return
	}
	randomBytes := make([]byte, 12)
	if _, err := rand.Read(randomBytes); err != nil {
		response.ReturnError(c, response.INTERNAL, "生成照片文件名失败")
		return
	}
	name := fmt.Sprintf("photo-%x%s", randomBytes, extension)
	if err := os.WriteFile(filepath.Join(articleimage.Directory, name), data, 0o644); err != nil {
		response.ReturnError(c, response.DATA_LOSS, "保存照片失败")
		return
	}
	response.ReturnData(c, gin.H{"url": articleimage.URLPrefix + name})
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
