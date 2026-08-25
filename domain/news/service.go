package news

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"api-server/config"
	"api-server/db/pgdb/system"
)

type CollectionService struct {
	db       *gorm.DB
	adapters map[string]Adapter
}

func NewCollectionService(db *gorm.DB) *CollectionService {
	service := &CollectionService{
		db:       db,
		adapters: make(map[string]Adapter),
	}
	service.RegisterAdapter(GovCNPushInfoAdapter{})
	service.RegisterAdapter(SinaFinanceAdapter{})
	return service
}

func (service *CollectionService) RegisterAdapter(adapter Adapter) {
	if adapter == nil {
		return
	}
	service.adapters[adapter.Key()] = adapter
}

func (service *CollectionService) CollectEnabledSources(ctx context.Context, trigger string) (CollectStats, error) {
	var sources []system.NewsSource
	if err := service.db.Where("enabled = ?", true).Order("id ASC").Find(&sources).Error; err != nil {
		return CollectStats{}, err
	}

	totalStats := CollectStats{}
	for _, source := range sources {
		stats, err := service.collectSingleSource(ctx, source, trigger)
		mergeStats(&totalStats, stats)
		if err != nil {
			zap.L().Warn("新闻来源采集失败", zap.Uint("source_id", source.ID), zap.String("source", source.Name), zap.Error(err))
		}
	}
	return totalStats, nil
}

func (service *CollectionService) CollectBySourceID(ctx context.Context, sourceID uint, trigger string) (CollectStats, error) {
	var source system.NewsSource
	if err := service.db.First(&source, sourceID).Error; err != nil {
		return CollectStats{}, err
	}
	return service.collectSingleSource(ctx, source, trigger)
}

func (service *CollectionService) collectSingleSource(ctx context.Context, source system.NewsSource, trigger string) (CollectStats, error) {
	startedAt := time.Now().UTC()
	logRow := system.NewsCollectLog{
		TaskID:    fmt.Sprintf("news-%d-%d", source.ID, startedAt.UnixNano()),
		SourceID:  source.ID,
		StartedAt: startedAt.Unix(),
		Status:    "running",
		TraceID:   "",
	}
	_ = service.db.Create(&logRow).Error

	locked, lockErr := service.trySourceLock(source.ID)
	if lockErr != nil {
		service.finishLog(logRow.ID, CollectStats{}, "failed", "acquire lock failed: "+lockErr.Error())
		return CollectStats{}, lockErr
	}
	if !locked {
		service.finishLog(logRow.ID, CollectStats{}, "skipped", "source is running on another instance")
		return CollectStats{}, nil
	}
	defer service.releaseSourceLock(source.ID)

	cfg := parseSourceConfig(source.ConfigJSON)
	if strings.TrimSpace(source.CategoryMapping) != "" && len(cfg.CategoryMapping) == 0 {
		mapping := make(map[string]string)
		if err := json.Unmarshal([]byte(source.CategoryMapping), &mapping); err == nil {
			cfg.CategoryMapping = mapping
		}
	}
	adapter := service.adapters[cfg.Adapter]
	if adapter == nil {
		service.finishLog(logRow.ID, CollectStats{}, "failed", "adapter not found: "+cfg.Adapter)
		return CollectStats{}, fmt.Errorf("adapter not found: %s", cfg.Adapter)
	}

	items, err := service.fetchWithRetry(ctx, adapter, source, cfg)
	if err != nil {
		service.markSourceFailure(source.ID)
		service.finishLog(logRow.ID, CollectStats{}, "failed", err.Error())
		return CollectStats{}, err
	}

	stats := CollectStats{FetchedCount: len(items)}
	for _, item := range items {
		if err := service.upsertNormalized(item, &stats); err != nil {
			stats.FailedCount++
			zap.L().Warn("写入新闻失败", zap.String("title", item.Title), zap.Error(err))
		}
	}

	service.markSourceSuccess(source.ID)
	service.finishLog(logRow.ID, stats, "success", "")
	return stats, nil
}

func (service *CollectionService) fetchWithRetry(ctx context.Context, adapter Adapter, source system.NewsSource, cfg SourceConfig) ([]NormalizedNews, error) {
	maxRetries := config.NewsMaxRetries
	if maxRetries <= 0 {
		maxRetries = 3
	}
	var lastErr error
	for attempt := 0; attempt <= maxRetries; attempt++ {
		items, err := adapter.Fetch(ctx, source, cfg)
		if err == nil {
			return items, nil
		}
		lastErr = err
		if !errors.Is(err, ErrRetryable) {
			break
		}
		if attempt == maxRetries {
			break
		}
		time.Sleep(time.Duration(1<<attempt) * 500 * time.Millisecond)
	}
	return nil, lastErr
}

func (service *CollectionService) upsertNormalized(item NormalizedNews, stats *CollectStats) error {
	title := CleanText(item.Title, 240)
	if title == "" {
		stats.FailedCount++
		return fmt.Errorf("empty title")
	}

	normalizedURL, err := NormalizeURL(item.SourceURL)
	if err != nil {
		stats.FailedCount++
		return err
	}
	if item.CollectedAt.IsZero() {
		item.CollectedAt = time.Now().UTC()
	}
	if item.PublishedAt.IsZero() {
		item.PublishedAt = time.Now().UTC()
	}

	urlHash := URLHash(normalizedURL)
	contentHash := ContentHash(title, item.SourceName, item.PublishedAt)
	if item.SourceUniqueID == "" {
		item.SourceUniqueID = urlHash
	}

	var existing system.FinanceNews
	err = service.db.Where("source_name = ? AND source_unique_id = ?", item.SourceName, item.SourceUniqueID).First(&existing).Error
	if err != nil {
		err = service.db.Where("url_hash = ?", urlHash).First(&existing).Error
	}
	if err != nil {
		windowStart := item.PublishedAt.Add(-6 * time.Hour).Unix()
		windowEnd := item.PublishedAt.Add(6 * time.Hour).Unix()
		err = service.db.Where("content_hash = ? AND published_at BETWEEN ? AND ?", contentHash, windowStart, windowEnd).First(&existing).Error
	}

	record := system.FinanceNews{
		Title:            title,
		Summary:          CleanText(item.Summary, 1200),
		Content:          CleanText(item.Content, 8000),
		SourceName:       CleanText(item.SourceName, 100),
		SourceURL:        normalizedURL,
		Author:           CleanText(item.Author, 120),
		Category:         item.Category,
		OriginalCategory: CleanText(item.OriginalCategory, 120),
		PublishedAt:      item.PublishedAt.Unix(),
		CollectedAt:      item.CollectedAt.Unix(),
		Language:         defaultString(item.Language, config.NewsDefaultLanguage),
		Region:           defaultString(item.Region, "CN"),
		ContentType:      defaultString(item.ContentType, ContentTypeArticle),
		CopyrightMode:    defaultString(item.CopyrightMode, CopyrightModeMetadataOnly),
		Status:           StatusPublished,
		Importance:       0,
		IsTop:            false,
		CoverImageURL:    CleanText(item.CoverImageURL, 1000),
		SourceUniqueID:   item.SourceUniqueID,
		URLHash:          urlHash,
		ContentHash:      contentHash,
		RawData:          item.RawData,
	}

	if err == nil && existing.ID > 0 {
		updates := map[string]interface{}{
			"summary":           record.Summary,
			"content":           record.Content,
			"author":            record.Author,
			"original_category": record.OriginalCategory,
			"published_at":      record.PublishedAt,
			"collected_at":      record.CollectedAt,
			"content_type":      record.ContentType,
			"copyright_mode":    record.CopyrightMode,
			"cover_image_url":   record.CoverImageURL,
			"raw_data":          record.RawData,
			"updated_at":        time.Now(),
		}
		if updateErr := service.db.Model(&system.FinanceNews{}).Where("id = ?", existing.ID).Updates(updates).Error; updateErr != nil {
			return updateErr
		}
		stats.UpdatedCount++
		stats.DuplicateCount++
		return service.syncRelations(existing.ID, item.SecurityCodes, item.Keywords)
	}
	if err != nil && !errors.Is(err, gorm.ErrRecordNotFound) {
		return err
	}

	if err := service.db.Create(&record).Error; err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "duplicate") || strings.Contains(strings.ToLower(err.Error()), "unique") {
			stats.DuplicateCount++
			return nil
		}
		return err
	}
	stats.InsertedCount++
	return service.syncRelations(record.ID, item.SecurityCodes, item.Keywords)
}

func (service *CollectionService) syncRelations(newsID uint, securityCodes []string, keywords []string) error {
	if newsID == 0 {
		return nil
	}
	if err := service.db.Where("news_id = ?", newsID).Delete(&system.NewsSecurityRelation{}).Error; err != nil {
		return err
	}
	if err := service.db.Where("news_id = ?", newsID).Delete(&system.NewsKeyword{}).Error; err != nil {
		return err
	}
	for _, code := range securityCodes {
		if len(strings.TrimSpace(code)) != 6 {
			continue
		}
		relation := system.NewsSecurityRelation{NewsID: newsID, SecurityCode: strings.TrimSpace(code), Market: "A", RelationType: "keyword", Confidence: 0.65}
		if err := service.db.Create(&relation).Error; err != nil {
			return err
		}
	}
	for _, keyword := range keywords {
		clean := CleanText(keyword, 80)
		if clean == "" {
			continue
		}
		if err := service.db.Create(&system.NewsKeyword{NewsID: newsID, Keyword: clean}).Error; err != nil {
			return err
		}
	}
	return nil
}

func (service *CollectionService) trySourceLock(sourceID uint) (bool, error) {
	var locked bool
	key := int64(764200 + sourceID)
	if err := service.db.Raw("SELECT pg_try_advisory_lock(?)", key).Scan(&locked).Error; err != nil {
		return false, err
	}
	return locked, nil
}

func (service *CollectionService) releaseSourceLock(sourceID uint) {
	key := int64(764200 + sourceID)
	_ = service.db.Exec("SELECT pg_advisory_unlock(?)", key).Error
}

func (service *CollectionService) finishLog(logID uint, stats CollectStats, status, errorSummary string) {
	if logID == 0 {
		return
	}
	updates := map[string]interface{}{
		"finished_at":     time.Now().UTC().Unix(),
		"status":          status,
		"fetched_count":   stats.FetchedCount,
		"inserted_count":  stats.InsertedCount,
		"updated_count":   stats.UpdatedCount,
		"duplicate_count": stats.DuplicateCount,
		"failed_count":    stats.FailedCount,
		"error_summary":   CleanText(errorSummary, 600),
		"updated_at":      time.Now(),
	}
	_ = service.db.Model(&system.NewsCollectLog{}).Where("id = ?", logID).Updates(updates).Error
}

func (service *CollectionService) markSourceFailure(sourceID uint) {
	now := time.Now().UTC().Unix()
	_ = service.db.Model(&system.NewsSource{}).
		Where("id = ?", sourceID).
		Updates(map[string]interface{}{"last_collected_at": now, "consecutive_failures": gorm.Expr("consecutive_failures + 1"), "updated_at": time.Now()}).Error
}

func (service *CollectionService) markSourceSuccess(sourceID uint) {
	now := time.Now().UTC().Unix()
	_ = service.db.Model(&system.NewsSource{}).
		Where("id = ?", sourceID).
		Updates(map[string]interface{}{"last_collected_at": now, "last_success_at": now, "consecutive_failures": 0, "updated_at": time.Now()}).Error
}

func parseSourceConfig(raw string) SourceConfig {
	cfg := SourceConfig{
		Adapter:          "gov_cn_pushinfo",
		Category:         CategoryPolicy,
		MaxItems:         30,
		RequestTimeoutMS: 10000,
		MaxResponseBytes: 2 * 1024 * 1024,
	}
	if strings.TrimSpace(raw) == "" {
		return cfg
	}
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return cfg
	}
	if cfg.Adapter == "" {
		cfg.Adapter = "gov_cn_pushinfo"
	}
	if cfg.MaxItems <= 0 {
		cfg.MaxItems = 30
	}
	if cfg.RequestTimeoutMS <= 0 {
		cfg.RequestTimeoutMS = 10000
	}
	if cfg.MaxResponseBytes <= 0 {
		cfg.MaxResponseBytes = 2 * 1024 * 1024
	}
	return cfg
}

func mergeStats(dst *CollectStats, src CollectStats) {
	dst.FetchedCount += src.FetchedCount
	dst.InsertedCount += src.InsertedCount
	dst.UpdatedCount += src.UpdatedCount
	dst.DuplicateCount += src.DuplicateCount
	dst.FailedCount += src.FailedCount
}

func defaultString(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
