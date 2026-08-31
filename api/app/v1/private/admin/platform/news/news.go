package news

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/config"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	newsdomain "api-server/domain/news"
)

func getCollector() *newsdomain.CollectionService {
	return newsdomain.NewCollectionService(pgdb.GetClient())
}

type newsUpdateInput struct {
	ID       uint   `json:"id"`
	Title    string `json:"title"`
	Category string `json:"category"`
	Summary  string `json:"summary"`
	Status   string `json:"status"`
	IsTop    *bool  `json:"is_top"`
}

type batchActionInput struct {
	Action string `json:"action"`
	IDs    []uint `json:"ids"`
}

type sourceUpdateInput struct {
	ID              uint   `json:"id"`
	Enabled         *bool  `json:"enabled"`
	IntervalSeconds int    `json:"interval_seconds"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	RateLimit       int    `json:"rate_limit"`
	ConfigJSON      string `json:"config_json"`
}

type sourceCreateInput struct {
	Name            string `json:"name" binding:"required"`
	SourceType      string `json:"source_type" binding:"required"`
	BaseURL         string `json:"base_url" binding:"required"`
	CategoryMapping string `json:"category_mapping"`
	Enabled         *bool  `json:"enabled"`
	IntervalSeconds int    `json:"interval_seconds"`
	TimeoutSeconds  int    `json:"timeout_seconds"`
	RateLimit       int    `json:"rate_limit"`
	ConfigJSON      string `json:"config_json"`
}

type collectInput struct {
	SourceID *uint `json:"source_id"`
}

func ListNews(c *gin.Context) {
	page, pageSize := middleware.GetPage(c), middleware.GetPageSize(c)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}

	db := pgdb.GetClient().Model(&system.FinanceNews{}).Where("deleted_at IS NULL")
	if keyword := strings.TrimSpace(c.Query("keyword")); keyword != "" {
		db = db.Where("title LIKE ? OR summary LIKE ?", "%"+keyword+"%", "%"+keyword+"%")
	}
	if category := strings.TrimSpace(c.Query("category")); category != "" {
		db = db.Where("category = ?", category)
	}
	if sourceName := strings.TrimSpace(c.Query("source")); sourceName != "" {
		db = db.Where("source_name = ?", sourceName)
	}
	if contentType := strings.TrimSpace(c.Query("contentType")); contentType != "" {
		db = db.Where("content_type = ?", contentType)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		db = db.Where("status = ?", status)
	}
	if code := strings.TrimSpace(c.Query("securityCode")); code != "" {
		db = db.Joins("JOIN news_security_relations nsr ON nsr.news_id = finance_news.id AND nsr.deleted_at IS NULL").Where("nsr.security_code = ?", code)
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

	var total int64
	if err := db.Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询新闻失败")
		return
	}

	var rows []system.FinanceNews
	if err := db.Order("is_top DESC, published_at DESC, id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询新闻失败")
		return
	}

	ids := make([]uint, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	relations := make(map[uint][]string)
	if len(ids) > 0 {
		var items []system.NewsSecurityRelation
		_ = pgdb.GetClient().Where("news_id IN ? AND deleted_at IS NULL", ids).Find(&items).Error
		for _, item := range items {
			relations[item.NewsID] = append(relations[item.NewsID], item.SecurityCode)
		}
	}

	view := make([]gin.H, 0, len(rows))
	for _, row := range rows {
		view = append(view, gin.H{
			"id":               row.ID,
			"title":            row.Title,
			"summary":          row.Summary,
			"source_name":      row.SourceName,
			"source_url":       row.SourceURL,
			"category":         row.Category,
			"content_type":     row.ContentType,
			"status":           row.Status,
			"is_top":           row.IsTop,
			"published_at":     row.PublishedAt,
			"collected_at":     row.CollectedAt,
			"security_codes":   relations[row.ID],
			"source_unique_id": row.SourceUniqueID,
			"copyright_mode":   row.CopyrightMode,
			"created_at":       row.CreatedAt,
			"updated_at":       row.UpdatedAt,
		})
	}

	response.ReturnData(c, gin.H{"records": view, "total": total, "page": page, "page_size": pageSize})
}

func GetNews(c *gin.Context) {
	id := strings.TrimSpace(c.Param("id"))
	if id == "" {
		id = strings.TrimSpace(c.Query("id"))
	}
	if id == "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "新闻ID不能为空")
		return
	}

	var row system.FinanceNews
	if err := pgdb.GetClient().Where("id = ? AND deleted_at IS NULL", id).First(&row).Error; err != nil {
		response.ReturnError(c, response.NOT_FOUND, "新闻不存在")
		return
	}

	var relations []system.NewsSecurityRelation
	_ = pgdb.GetClient().Where("news_id = ? AND deleted_at IS NULL", row.ID).Find(&relations).Error
	codes := make([]string, 0, len(relations))
	for _, item := range relations {
		codes = append(codes, item.SecurityCode)
	}

	var keywords []system.NewsKeyword
	_ = pgdb.GetClient().Where("news_id = ? AND deleted_at IS NULL", row.ID).Find(&keywords).Error
	keywordValues := make([]string, 0, len(keywords))
	for _, item := range keywords {
		keywordValues = append(keywordValues, item.Keyword)
	}

	response.ReturnData(c, gin.H{
		"id":                row.ID,
		"title":             row.Title,
		"summary":           row.Summary,
		"content":           row.Content,
		"source_name":       row.SourceName,
		"source_url":        row.SourceURL,
		"author":            row.Author,
		"category":          row.Category,
		"original_category": row.OriginalCategory,
		"published_at":      row.PublishedAt,
		"collected_at":      row.CollectedAt,
		"language":          row.Language,
		"region":            row.Region,
		"content_type":      row.ContentType,
		"copyright_mode":    row.CopyrightMode,
		"status":            row.Status,
		"importance":        row.Importance,
		"is_top":            row.IsTop,
		"source_unique_id":  row.SourceUniqueID,
		"url_hash":          row.URLHash,
		"content_hash":      row.ContentHash,
		"security_codes":    codes,
		"keywords":          keywordValues,
		"raw_data":          row.RawData,
		"created_at":        row.CreatedAt,
		"updated_at":        row.UpdatedAt,
	})
}

func UpdateNews(c *gin.Context) {
	var input newsUpdateInput
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "新闻ID无效")
		return
	}

	updates := map[string]interface{}{}
	if title := strings.TrimSpace(input.Title); title != "" {
		updates["title"] = title
	}
	if category := strings.TrimSpace(input.Category); category != "" {
		updates["category"] = category
	}
	if summary := strings.TrimSpace(input.Summary); summary != "" {
		updates["summary"] = summary
	}
	if status := strings.TrimSpace(input.Status); status != "" {
		updates["status"] = status
	}
	if input.IsTop != nil {
		updates["is_top"] = *input.IsTop
	}
	if len(updates) == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "没有可更新字段")
		return
	}

	if err := pgdb.GetClient().Model(&system.FinanceNews{}).Where("id = ? AND deleted_at IS NULL", input.ID).Updates(updates).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "更新新闻失败")
		return
	}
	response.ReturnSuccess(c)
}

func DeleteNews(c *gin.Context) {
	var input struct {
		ID uint `json:"id"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "新闻ID无效")
		return
	}
	if err := pgdb.GetClient().Delete(&system.FinanceNews{}, input.ID).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "删除新闻失败")
		return
	}
	response.ReturnSuccess(c)
}

func BatchAction(c *gin.Context) {
	var input batchActionInput
	if !middleware.CheckParam(&input, c) || len(input.IDs) == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "批量参数无效")
		return
	}
	if input.Action == "hide" {
		if err := pgdb.GetClient().Model(&system.FinanceNews{}).Where("id IN ?", input.IDs).Update("status", newsdomain.StatusHidden).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "批量隐藏失败")
			return
		}
		response.ReturnSuccess(c)
		return
	}
	if input.Action == "delete" {
		if err := pgdb.GetClient().Where("id IN ?", input.IDs).Delete(&system.FinanceNews{}).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "批量删除失败")
			return
		}
		response.ReturnSuccess(c)
		return
	}
	response.ReturnError(c, response.INVALID_ARGUMENT, "不支持的批量动作")
}

func ListSources(c *gin.Context) {
	var rows []system.NewsSource
	if err := pgdb.GetClient().Order("id ASC").Find(&rows).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询新闻源失败")
		return
	}
	response.ReturnData(c, rows)
}

func CreateSource(c *gin.Context) {
	var input sourceCreateInput
	if !middleware.CheckParam(&input, c) {
		return
	}
	if strings.TrimSpace(input.ConfigJSON) != "" {
		var config map[string]interface{}
		if err := json.Unmarshal([]byte(input.ConfigJSON), &config); err != nil {
			response.ReturnError(c, response.INVALID_ARGUMENT, "采集配置必须是有效JSON")
			return
		}
	}
	enabled := true
	if input.Enabled != nil {
		enabled = *input.Enabled
	}
	interval := input.IntervalSeconds
	if interval <= 0 {
		interval = 600
	}
	timeout := input.TimeoutSeconds
	if timeout <= 0 {
		timeout = 10
	}
	rateLimit := input.RateLimit
	if rateLimit <= 0 {
		rateLimit = 20
	}
	source := system.NewsSource{
		Name:            strings.TrimSpace(input.Name),
		SourceType:      strings.TrimSpace(input.SourceType),
		BaseURL:         strings.TrimSpace(input.BaseURL),
		CategoryMapping: strings.TrimSpace(input.CategoryMapping),
		Enabled:         enabled,
		IntervalSeconds: interval,
		TimeoutSeconds:  timeout,
		RateLimit:       rateLimit,
		ConfigJSON:      strings.TrimSpace(input.ConfigJSON),
	}
	if err := pgdb.GetClient().Create(&source).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "新增新闻源失败")
		return
	}
	response.ReturnData(c, gin.H{"id": source.ID})
}

func UpdateSource(c *gin.Context) {
	var input sourceUpdateInput
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "新闻源ID无效")
		return
	}
	updates := map[string]interface{}{}
	if input.Enabled != nil {
		updates["enabled"] = *input.Enabled
	}
	if input.IntervalSeconds > 0 {
		updates["interval_seconds"] = input.IntervalSeconds
	}
	if input.TimeoutSeconds > 0 {
		updates["timeout_seconds"] = input.TimeoutSeconds
	}
	if input.RateLimit > 0 {
		updates["rate_limit"] = input.RateLimit
	}
	if strings.TrimSpace(input.ConfigJSON) != "" {
		updates["config_json"] = input.ConfigJSON
	}
	if len(updates) == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "没有可更新字段")
		return
	}
	if err := pgdb.GetClient().Model(&system.NewsSource{}).Where("id = ?", input.ID).Updates(updates).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "更新新闻源失败")
		return
	}
	response.ReturnSuccess(c)
}

func DeleteSource(c *gin.Context) {
	var input struct {
		ID uint `json:"id" form:"id"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "新闻源ID无效")
		return
	}
	result := pgdb.GetClient().Delete(&system.NewsSource{}, input.ID)
	if result.Error != nil {
		response.ReturnError(c, response.DATA_LOSS, "删除新闻源失败")
		return
	}
	if result.RowsAffected == 0 {
		response.ReturnError(c, response.NOT_FOUND, "新闻源不存在")
		return
	}
	response.ReturnSuccess(c)
}

func CollectNews(c *gin.Context) {
	var input collectInput
	if !middleware.CheckParam(&input, c) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "参数错误")
		return
	}
	ctx, cancel := context.WithTimeout(c.Request.Context(), time.Duration(config.NewsRequestTimeoutMS)*time.Millisecond)
	defer cancel()

	collector := getCollector()
	if input.SourceID == nil {
		stats, err := collector.CollectEnabledSources(ctx, "manual")
		if err != nil {
			response.ReturnError(c, response.DATA_LOSS, "手动采集失败: "+err.Error())
			return
		}
		response.ReturnData(c, stats)
		return
	}

	stats, err := collector.CollectBySourceID(ctx, *input.SourceID, "manual")
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "手动采集失败: "+err.Error())
		return
	}
	response.ReturnData(c, stats)
}

func ListCollectLogs(c *gin.Context) {
	page, pageSize := middleware.GetPage(c), middleware.GetPageSize(c)
	if page < 1 {
		page = 1
	}
	if pageSize < 1 {
		pageSize = 20
	}
	if pageSize > 100 {
		pageSize = 100
	}
	query := pgdb.GetClient().Model(&system.NewsCollectLog{}).Where("deleted_at IS NULL")
	if sourceID := strings.TrimSpace(c.Query("source_id")); sourceID != "" {
		query = query.Where("source_id = ?", sourceID)
	}
	if status := strings.TrimSpace(c.Query("status")); status != "" {
		query = query.Where("status = ?", status)
	}
	var total int64
	if err := query.Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询采集日志失败")
		return
	}
	var rows []system.NewsCollectLog
	if err := query.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&rows).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询采集日志失败")
		return
	}
	response.ReturnData(c, gin.H{"records": rows, "total": total})
}
