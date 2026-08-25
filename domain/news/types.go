package news

import "time"

const (
	CategoryFinance       = "FINANCE"
	CategoryEconomy       = "ECONOMY"
	CategoryFlash         = "FLASH"
	CategoryCommodity     = "COMMODITY"
	CategoryListedCompany = "LISTED_COMPANY"
	CategoryCentralBank   = "CENTRAL_BANK"
	CategoryAShare        = "A_SHARE"
	CategoryHKStock       = "HK_STOCK"
	CategoryUSStock       = "US_STOCK"
	CategoryIndustry      = "INDUSTRY"
	CategoryAnnouncement  = "ANNOUNCEMENT"
	CategoryPolicy        = "POLICY"
	CategoryOther         = "OTHER"
)

const (
	ContentTypeArticle      = "article"
	ContentTypeFlash        = "flash"
	ContentTypeAnnouncement = "announcement"
)

const (
	CopyrightModeFull         = "full"
	CopyrightModeExcerpt      = "excerpt"
	CopyrightModeMetadataOnly = "metadata_only"
)

const (
	StatusDraft     = "draft"
	StatusPublished = "published"
	StatusHidden    = "hidden"
)

type NormalizedNews struct {
	SourceName       string
	SourceUniqueID   string
	SourceURL        string
	CoverImageURL    string
	Title            string
	Summary          string
	Content          string
	Author           string
	Category         string
	OriginalCategory string
	ContentType      string
	PublishedAt      time.Time
	CollectedAt      time.Time
	Keywords         []string
	SecurityCodes    []string
	Region           string
	Language         string
	CopyrightMode    string
	RawData          string
}

type SourceConfig struct {
	Adapter             string            `json:"adapter"`
	Category            string            `json:"category"`
	CategoryMapping     map[string]string `json:"category_mapping"`
	IncludeContent      bool              `json:"include_content"`
	MaxItems            int               `json:"max_items"`
	RequestTimeoutMS    int               `json:"request_timeout_ms"`
	MaxResponseBytes    int64             `json:"max_response_bytes"`
	ExtractSecurityCode bool              `json:"extract_security_code"`
}

type CollectStats struct {
	FetchedCount   int
	InsertedCount  int
	UpdatedCount   int
	DuplicateCount int
	FailedCount    int
}
