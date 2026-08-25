package mobile

import (
	"errors"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

type publicNewsItem struct {
	ID            uint     `json:"id"`
	Title         string   `json:"title"`
	Summary       string   `json:"summary"`
	SourceName    string   `json:"sourceName"`
	SourceURL     string   `json:"sourceUrl"`
	CoverImageURL string   `json:"coverImageUrl,omitempty"`
	Category      string   `json:"category"`
	ContentType   string   `json:"contentType"`
	PublishedAt   int64    `json:"publishedAt"`
	SecurityCodes []string `json:"securityCodes"`
	CopyrightMode string   `json:"copyrightMode"`
}

func ListNews(c *gin.Context) {
	page, pageSize := middleware.GetPage(c), middleware.GetPageSize(c)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 50 {
		pageSize = 50
	}

	db := pgdb.GetClient().Model(&system.FinanceNews{}).Where("deleted_at IS NULL AND status = ?", "published")
	if category := strings.TrimSpace(c.Query("category")); category != "" {
		db = db.Where("category = ?", category)
	}
	if contentType := strings.TrimSpace(c.Query("contentType")); contentType != "" {
		db = db.Where("content_type = ?", contentType)
	}
	if sourceName := strings.TrimSpace(c.Query("source")); sourceName != "" {
		db = db.Where("source_name = ?", sourceName)
	}
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		db = db.Where("title LIKE ? OR summary LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	if start := strings.TrimSpace(c.Query("startTime")); start != "" {
		if value, err := strconv.ParseInt(start, 10, 64); err == nil {
			db = db.Where("published_at >= ?", value)
		}
	}
	if end := strings.TrimSpace(c.Query("endTime")); end != "" {
		if value, err := strconv.ParseInt(end, 10, 64); err == nil {
			db = db.Where("published_at <= ?", value)
		}
	}

	if code := strings.TrimSpace(c.Query("securityCode")); code != "" {
		db = db.Joins("JOIN news_security_relations nsr ON nsr.news_id = finance_news.id AND nsr.deleted_at IS NULL").Where("nsr.security_code = ?", code)
	}

	var total int64
	if err := db.Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取新闻失败")
		return
	}
	var rows []system.FinanceNews
	if err := db.Order("is_top DESC, published_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取新闻失败")
		return
	}
	response.ReturnData(c, gin.H{"records": buildNewsItems(rows), "total": total, "page": page, "page_size": pageSize})
}

func GetNewsByID(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	var row system.FinanceNews
	err := pgdb.GetClient().Where("id = ? AND deleted_at IS NULL AND status = ?", id, "published").First(&row).Error
	if errors.Is(err, gorm.ErrRecordNotFound) {
		response.ReturnError(c, response.NOT_FOUND, "新闻不存在")
		return
	}
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取新闻失败")
		return
	}
	codes := fetchNewsSecurityCodes([]uint{row.ID})
	response.ReturnData(c, gin.H{
		"id":            row.ID,
		"title":         row.Title,
		"summary":       row.Summary,
		"content":       row.Content,
		"sourceName":    row.SourceName,
		"sourceUrl":     row.SourceURL,
		"coverImageUrl": row.CoverImageURL,
		"author":        row.Author,
		"category":      row.Category,
		"contentType":   row.ContentType,
		"publishedAt":   row.PublishedAt,
		"securityCodes": codes[row.ID],
		"copyrightMode": row.CopyrightMode,
	})
}

func ListNewsCategories(c *gin.Context) {
	response.ReturnData(c, []gin.H{
		{"code": "FINANCE", "label": "财经"},
		{"code": "ECONOMY", "label": "经济"},
		{"code": "FLASH", "label": "7×24"},
		{"code": "COMMODITY", "label": "商品"},
		{"code": "LISTED_COMPANY", "label": "上市公司"},
		{"code": "CENTRAL_BANK", "label": "央行"},
		{"code": "A_SHARE", "label": "A股"},
		{"code": "HK_STOCK", "label": "港股"},
		{"code": "US_STOCK", "label": "美股"},
		{"code": "INDUSTRY", "label": "行业资讯"},
		{"code": "ANNOUNCEMENT", "label": "公司公告"},
		{"code": "POLICY", "label": "宏观政策"},
		{"code": "OTHER", "label": "其他"},
	})
}

func ListSecurityNews(c *gin.Context) {
	code := strings.TrimSpace(c.Param("code"))
	if len(code) == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "证券代码不能为空")
		return
	}
	page, pageSize := middleware.GetPage(c), middleware.GetPageSize(c)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 50 {
		pageSize = 50
	}

	db := pgdb.GetClient().Model(&system.FinanceNews{}).
		Joins("JOIN news_security_relations nsr ON nsr.news_id = finance_news.id AND nsr.deleted_at IS NULL").
		Where("finance_news.deleted_at IS NULL AND finance_news.status = ? AND nsr.security_code = ?", "published", code)

	var total int64
	if err := db.Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取证券相关新闻失败")
		return
	}
	var rows []system.FinanceNews
	if err := db.Order("finance_news.published_at DESC, finance_news.id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取证券相关新闻失败")
		return
	}
	response.ReturnData(c, gin.H{"records": buildNewsItems(rows), "total": total, "page": page, "page_size": pageSize})
}

func buildNewsItems(rows []system.FinanceNews) []publicNewsItem {
	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	codeMap := fetchNewsSecurityCodes(ids)
	items := make([]publicNewsItem, 0, len(rows))
	for _, row := range rows {
		items = append(items, publicNewsItem{
			ID:            row.ID,
			Title:         row.Title,
			Summary:       row.Summary,
			SourceName:    row.SourceName,
			SourceURL:     row.SourceURL,
			CoverImageURL: row.CoverImageURL,
			Category:      row.Category,
			ContentType:   row.ContentType,
			PublishedAt:   row.PublishedAt,
			SecurityCodes: codeMap[row.ID],
			CopyrightMode: row.CopyrightMode,
		})
	}
	return items
}

func fetchNewsSecurityCodes(ids []uint) map[uint][]string {
	result := map[uint][]string{}
	if len(ids) == 0 {
		return result
	}
	var rows []system.NewsSecurityRelation
	if err := pgdb.GetClient().Where("news_id IN ? AND deleted_at IS NULL", ids).Find(&rows).Error; err != nil {
		return result
	}
	for _, row := range rows {
		result[row.NewsID] = append(result[row.NewsID], row.SecurityCode)
	}
	return result
}
