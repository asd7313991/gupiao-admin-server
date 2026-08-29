package setting

import (
	"crypto/rand"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

func GetSystemSetting(c *gin.Context) {
	var setting system.AppSystemSetting
	if err := pgdb.GetClient().First(&setting).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取系统设置失败")
		return
	}
	var config any
	if err := json.Unmarshal([]byte(setting.Config), &config); err != nil {
		response.ReturnError(c, response.DATA_LOSS, "系统设置格式错误")
		return
	}
	response.ReturnData(c, config)
}

func SaveSystemSetting(c *gin.Context) {
	var config any
	if !middleware.CheckParam(&config, c) {
		return
	}
	content, err := json.Marshal(config)
	if err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "系统设置格式错误")
		return
	}
	var setting system.AppSystemSetting
	if err := pgdb.GetClient().First(&setting).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取系统设置失败")
		return
	}
	if err := pgdb.GetClient().Model(&setting).Update("config", string(content)).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "保存系统设置失败")
		return
	}
	response.ReturnData(c, nil)
}

func UploadBrandLogo(c *gin.Context) {
	file, err := c.FormFile("file")
	if err != nil || file.Size <= 0 || file.Size > 2*1024*1024 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请选择不超过 2MB 的 Logo 图片")
		return
	}
	source, err := file.Open()
	if err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "读取 Logo 图片失败")
		return
	}
	defer source.Close()
	header := make([]byte, 512)
	read, _ := source.Read(header)
	contentType := http.DetectContentType(header[:read])
	extension := map[string]string{
		"image/jpeg": ".jpg",
		"image/png":  ".png",
		"image/webp": ".webp",
	}[contentType]
	if extension == "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "Logo 仅支持 JPEG、PNG 或 WebP 格式")
		return
	}
	directory := filepath.Join("static", "branding")
	if err := os.MkdirAll(directory, 0o755); err != nil {
		response.ReturnError(c, response.DATA_LOSS, "创建 Logo 存储目录失败")
		return
	}
	randomBytes := make([]byte, 12)
	if _, err := rand.Read(randomBytes); err != nil {
		response.ReturnError(c, response.INTERNAL, "生成 Logo 文件名失败")
		return
	}
	name := fmt.Sprintf("logo-%x%s", randomBytes, extension)
	if err := c.SaveUploadedFile(file, filepath.Join(directory, name)); err != nil {
		response.ReturnError(c, response.DATA_LOSS, "保存 Logo 图片失败")
		return
	}
	response.ReturnData(c, gin.H{
		"logo":     name,
		"logo_url": "/api/v1/open/mobile/branding/logo?v=" + name,
	})
}
