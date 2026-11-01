package mobile

import (
	"errors"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"api-server/util/articleimage"
)

var mobileArticleTypes = map[string]struct{}{
	"新手必看": {},
	"法律声明": {},
	"帮助中心": {},
}

type mobileArticleView struct {
	Exists       bool     `json:"exists"`
	Title        string   `json:"title"`
	Type         string   `json:"type"`
	Content      string   `json:"content"`
	FooterImages []string `json:"footer_images"`
	UpdatedAt    int64    `json:"updated_at"`
}

func MobileArticle(c *gin.Context) {
	articleType := strings.TrimSpace(c.Query("type"))
	if _, ok := mobileArticleTypes[articleType]; !ok {
		response.ReturnError(c, response.INVALID_ARGUMENT, "文章类型无效")
		return
	}

	var article system.AppArticle
	err := pgdb.GetClient().Where("type = ? AND status = ? AND deleted_at IS NULL", articleType, system.StatusEnabled).Order("id DESC").First(&article).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.ReturnData(c, mobileArticleView{Exists: false, Type: articleType})
		return
	}
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取文章失败")
		return
	}

	response.ReturnData(c, mobileArticleView{
		Exists:       true,
		Title:        article.Title,
		Type:         article.Type,
		Content:      article.Content,
		FooterImages: article.FooterImages,
		UpdatedAt:    article.UpdatedAt.Unix(),
	})
}

func MobileArticleImage(c *gin.Context) {
	name := c.Param("name")
	if !articleimage.ValidName(name) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "照片地址无效")
		return
	}
	path := filepath.Join(articleimage.Directory, name)
	info, err := os.Stat(path)
	if errors.Is(err, os.ErrNotExist) {
		c.Status(http.StatusNotFound)
		return
	}
	if err != nil || !info.Mode().IsRegular() {
		response.ReturnError(c, response.DATA_LOSS, "读取照片失败")
		return
	}
	c.Header("X-Content-Type-Options", "nosniff")
	c.Header("Cache-Control", "public, max-age=31536000, immutable")
	c.File(path)
}
