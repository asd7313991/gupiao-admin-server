package mobile

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/gin-gonic/gin"

	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

const defaultProductName = "证券行情"

type brandingSetting struct {
	Branding struct {
		ProductName string `json:"productName"`
		Logo        string `json:"logo"`
	} `json:"branding"`
}

func MobileBranding(c *gin.Context) {
	branding, ok := loadBranding(c)
	if !ok {
		return
	}
	productName := strings.TrimSpace(branding.Branding.ProductName)
	if productName == "" {
		productName = defaultProductName
	}
	logoURL := ""
	if validBrandLogoName(branding.Branding.Logo) {
		logoURL = "/api/v1/open/mobile/branding/logo?v=" + branding.Branding.Logo
	}
	response.ReturnData(c, gin.H{"product_name": productName, "logo_url": logoURL})
}

func MobileBrandLogo(c *gin.Context) {
	branding, ok := loadBranding(c)
	if !ok {
		return
	}
	if !validBrandLogoName(branding.Branding.Logo) {
		c.Status(http.StatusNotFound)
		return
	}
	path := filepath.Join("static", "branding", branding.Branding.Logo)
	if _, err := os.Stat(path); err != nil {
		c.Status(http.StatusNotFound)
		return
	}
	c.Header("Cache-Control", "public, max-age=86400")
	c.File(path)
}

func loadBranding(c *gin.Context) (brandingSetting, bool) {
	var result brandingSetting
	var setting system.AppSystemSetting
	if err := pgdb.GetClient().First(&setting).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取品牌配置失败")
		return result, false
	}
	if err := json.Unmarshal([]byte(setting.Config), &result); err != nil {
		response.ReturnError(c, response.DATA_LOSS, "品牌配置格式错误")
		return result, false
	}
	return result, true
}

func validBrandLogoName(name string) bool {
	name = strings.TrimSpace(name)
	if name == "" || filepath.Base(name) != name {
		return false
	}
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".png", ".webp":
		return true
	default:
		return false
	}
}
