package news

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type YicaiHotAdapter struct{}

type yicaiHotItem struct {
	ChannelName string `json:"ChannelName"`
	CreateDate  string `json:"CreateDate"`
	NewsAuthor  string `json:"NewsAuthor"`
	NewsID      uint64 `json:"NewsID"`
	NewsNotes   string `json:"NewsNotes"`
	NewsSource  string `json:"NewsSource"`
	NewsThumbs  string `json:"NewsThumbs"`
	NewsTitle   string `json:"NewsTitle"`
	OriginPic   string `json:"originPic"`
	URL         string `json:"url"`
}

func (adapter YicaiHotAdapter) Key() string {
	return "yicai_hot"
}

func (adapter YicaiHotAdapter) Fetch(ctx context.Context, source SourceReader, cfg SourceConfig) ([]NormalizedNews, error) {
	timeout := 10 * time.Second
	if source.GetTimeoutSeconds() > 0 {
		timeout = time.Duration(source.GetTimeoutSeconds()) * time.Second
	} else if cfg.RequestTimeoutMS > 0 {
		timeout = time.Duration(cfg.RequestTimeoutMS) * time.Millisecond
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.GetBaseURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("Accept", "application/json")
	request.Header.Set("Referer", "https://www.yicai.com/")
	request.Header.Set("User-Agent", "stock-news-collector/1.0")

	response, err := (&http.Client{Timeout: timeout}).Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRetryable, err)
	}
	defer response.Body.Close()
	if response.StatusCode >= 500 {
		return nil, fmt.Errorf("%w: upstream %d", ErrRetryable, response.StatusCode)
	}
	if response.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", response.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(response.Body, responseLimit(cfg)))
	if err != nil {
		return nil, fmt.Errorf("%w: read body failed: %v", ErrRetryable, err)
	}
	var items []yicaiHotItem
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode json failed: %w", err)
	}

	maxItems := cfg.MaxItems
	if maxItems <= 0 || maxItems > len(items) {
		maxItems = len(items)
	}
	category := cfg.Category
	if category == "" {
		category = CategoryFinance
	}
	result := make([]NormalizedNews, 0, maxItems)
	for _, item := range items[:maxItems] {
		title := CleanText(item.NewsTitle, 240)
		if title == "" || item.NewsID == 0 {
			continue
		}
		itemCategory := NormalizeCategory(item.ChannelName, cfg.CategoryMapping)
		if itemCategory == CategoryOther {
			itemCategory = category
		}
		summary := CleanText(item.NewsNotes, 600)
		publishedAt := parseYicaiTime(item.CreateDate)
		if publishedAt.IsZero() {
			publishedAt = time.Now().UTC()
		}
		result = append(result, NormalizedNews{
			SourceName:       defaultString(CleanText(item.NewsSource, 100), source.GetName()),
			SourceUniqueID:   fmt.Sprintf("yicai-%d", item.NewsID),
			SourceURL:        "https://www.yicai.com" + strings.TrimSpace(item.URL),
			CoverImageURL:    firstImage(strings.TrimSpace(item.OriginPic), strings.TrimSpace(item.NewsThumbs)),
			Title:            title,
			Summary:          summary,
			Content:          summary,
			Author:           CleanText(item.NewsAuthor, 120),
			Category:         itemCategory,
			OriginalCategory: CleanText(item.ChannelName, 120),
			ContentType:      ContentTypeArticle,
			PublishedAt:      publishedAt,
			CollectedAt:      time.Now().UTC(),
			Region:           "CN",
			Language:         "zh-CN",
			CopyrightMode:    CopyrightModeMetadataOnly,
			RawData:          truncateRaw(body, 1200),
		})
	}
	return result, nil
}

func parseYicaiTime(value string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02T15:04:05", strings.TrimSpace(value), time.Local)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
