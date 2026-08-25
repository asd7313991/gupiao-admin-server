package system

import "gorm.io/gorm"

// FinanceNews 保存标准化后的财经新闻。
type FinanceNews struct {
	gorm.Model
	Title            string `json:"title" gorm:"type:varchar(240);not null;index"`
	Summary          string `json:"summary" gorm:"type:text"`
	Content          string `json:"content" gorm:"type:text"`
	SourceName       string `json:"source_name" gorm:"type:varchar(100);not null;index:idx_news_source_time"`
	SourceURL        string `json:"source_url" gorm:"type:text"`
	Author           string `json:"author" gorm:"type:varchar(120)"`
	Category         string `json:"category" gorm:"type:varchar(40);index:idx_news_category_time"`
	OriginalCategory string `json:"original_category" gorm:"type:varchar(120)"`
	PublishedAt      int64  `json:"published_at" gorm:"index;index:idx_news_category_time;index:idx_news_status_time;index:idx_news_source_time"`
	CollectedAt      int64  `json:"collected_at" gorm:"index"`
	Language         string `json:"language" gorm:"type:varchar(20)"`
	Region           string `json:"region" gorm:"type:varchar(20)"`
	ContentType      string `json:"content_type" gorm:"type:varchar(20);index"`
	CopyrightMode    string `json:"copyright_mode" gorm:"type:varchar(20)"`
	Status           string `json:"status" gorm:"type:varchar(20);default:'published';index;index:idx_news_status_time"`
	Importance       int    `json:"importance" gorm:"default:0;index"`
	IsTop            bool   `json:"is_top" gorm:"default:false;index"`
	CoverImageURL    string `json:"cover_image_url" gorm:"type:text"`
	SourceUniqueID   string `json:"source_unique_id" gorm:"type:varchar(200);index:idx_news_source_unique,unique"`
	URLHash          string `json:"url_hash" gorm:"type:char(64);index:idx_news_url_hash,unique"`
	ContentHash      string `json:"content_hash" gorm:"type:char(64);index"`
	RawData          string `json:"raw_data" gorm:"type:text"`
}

// NewsSecurityRelation 保存新闻与证券代码关系。
type NewsSecurityRelation struct {
	gorm.Model
	NewsID       uint    `json:"news_id" gorm:"index:idx_news_security,uniqueIndex:idx_news_security;not null"`
	SecurityID   uint    `json:"security_id" gorm:"index"`
	SecurityCode string  `json:"security_code" gorm:"type:varchar(20);index:idx_news_security,uniqueIndex:idx_news_security;index"`
	Market       string  `json:"market" gorm:"type:varchar(20);index:idx_news_security,uniqueIndex:idx_news_security"`
	RelationType string  `json:"relation_type" gorm:"type:varchar(30)"`
	Confidence   float64 `json:"confidence"`
}

// NewsKeyword 保存新闻关键词。
type NewsKeyword struct {
	gorm.Model
	NewsID  uint   `json:"news_id" gorm:"index:idx_news_keyword,uniqueIndex:idx_news_keyword;not null"`
	Keyword string `json:"keyword" gorm:"type:varchar(120);index:idx_news_keyword,uniqueIndex:idx_news_keyword"`
}

// NewsSource 保存采集源配置。
type NewsSource struct {
	gorm.Model
	Name                string `json:"name" gorm:"type:varchar(120);not null;uniqueIndex"`
	SourceType          string `json:"source_type" gorm:"type:varchar(20);not null"`
	BaseURL             string `json:"base_url" gorm:"type:text;not null"`
	CategoryMapping     string `json:"category_mapping" gorm:"type:text"`
	Enabled             bool   `json:"enabled" gorm:"default:true;index"`
	IntervalSeconds     int    `json:"interval_seconds" gorm:"default:600"`
	TimeoutSeconds      int    `json:"timeout_seconds" gorm:"default:10"`
	RateLimit           int    `json:"rate_limit" gorm:"default:20"`
	LastCollectedAt     int64  `json:"last_collected_at"`
	LastSuccessAt       int64  `json:"last_success_at"`
	ConsecutiveFailures int    `json:"consecutive_failures" gorm:"default:0"`
	ConfigJSON          string `json:"config_json" gorm:"type:text"`
}

func (source NewsSource) GetID() uint {
	return source.Model.ID
}

func (source NewsSource) GetName() string {
	return source.Name
}

func (source NewsSource) GetBaseURL() string {
	return source.BaseURL
}

func (source NewsSource) GetCategoryMappingRaw() string {
	return source.CategoryMapping
}

func (source NewsSource) GetConfigJSONRaw() string {
	return source.ConfigJSON
}

func (source NewsSource) GetTimeoutSeconds() int {
	return source.TimeoutSeconds
}

// NewsCollectLog 保存每次采集批次日志。
type NewsCollectLog struct {
	gorm.Model
	TaskID         string `json:"task_id" gorm:"type:varchar(120);index"`
	SourceID       uint   `json:"source_id" gorm:"index"`
	StartedAt      int64  `json:"started_at" gorm:"index"`
	FinishedAt     int64  `json:"finished_at"`
	Status         string `json:"status" gorm:"type:varchar(20);index"`
	FetchedCount   int    `json:"fetched_count"`
	InsertedCount  int    `json:"inserted_count"`
	UpdatedCount   int    `json:"updated_count"`
	DuplicateCount int    `json:"duplicate_count"`
	FailedCount    int    `json:"failed_count"`
	ErrorSummary   string `json:"error_summary" gorm:"type:text"`
	TraceID        string `json:"trace_id" gorm:"type:varchar(80)"`
}
