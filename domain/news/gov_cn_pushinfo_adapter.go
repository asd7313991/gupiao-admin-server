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

type GovCNPushInfoAdapter struct{}

type govPushItem struct {
	Author      string   `json:"author"`
	Description string   `json:"description"`
	Image       string   `json:"image"`
	ImageURL    string   `json:"image_url"`
	Thumbnail   string   `json:"thumbnail"`
	Images      []string `json:"images"`
	Link        string   `json:"link"`
	PubDate     string   `json:"pubDate"`
	Title       string   `json:"title"`
	Category    string   `json:"category"`
}

func (adapter GovCNPushInfoAdapter) Key() string {
	return "gov_cn_pushinfo"
}

func (adapter GovCNPushInfoAdapter) Fetch(ctx context.Context, source SourceReader, cfg SourceConfig) ([]NormalizedNews, error) {
	timeout := 10 * time.Second
	if source.GetTimeoutSeconds() > 0 {
		timeout = time.Duration(source.GetTimeoutSeconds()) * time.Second
	} else if cfg.RequestTimeoutMS > 0 {
		timeout = time.Duration(cfg.RequestTimeoutMS) * time.Millisecond
	}

	maxResponseBytes := int64(2 * 1024 * 1024)
	if cfg.MaxResponseBytes > 0 {
		maxResponseBytes = cfg.MaxResponseBytes
	}

	request, err := http.NewRequestWithContext(ctx, http.MethodGet, source.GetBaseURL(), nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	request.Header.Set("User-Agent", "stock-news-collector/1.0 (+compliance)")
	request.Header.Set("Accept", "application/json,text/plain,*/*")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRetryable, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("%w: upstream %d", ErrRetryable, resp.StatusCode)
	}
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests {
		return nil, fmt.Errorf("upstream rejected request: %d", resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes))
	if err != nil {
		return nil, fmt.Errorf("%w: read body failed: %v", ErrRetryable, err)
	}

	items := make([]govPushItem, 0)
	if err := json.Unmarshal(body, &items); err != nil {
		return nil, fmt.Errorf("decode json failed: %w", err)
	}

	if cfg.MaxItems > 0 && len(items) > cfg.MaxItems {
		items = items[:cfg.MaxItems]
	}

	result := make([]NormalizedNews, 0, len(items))
	for _, item := range items {
		title := CleanText(item.Title, 240)
		if title == "" {
			continue
		}
		link, err := NormalizeURL(item.Link)
		if err != nil {
			continue
		}
		categoryRaw := strings.TrimSpace(item.Category)
		if categoryRaw == "" {
			categoryRaw = cfg.Category
		}
		category := NormalizeCategory(categoryRaw, cfg.CategoryMapping)
		summary := CleanText(item.Description, 600)

		content := ""
		copyrightMode := CopyrightModeMetadataOnly
		if summary != "" {
			content = summary
			copyrightMode = CopyrightModeExcerpt
		}
		if cfg.IncludeContent {
			content = summary
			copyrightMode = CopyrightModeFull
		}

		normalized := NormalizedNews{
			SourceName:       source.GetName(),
			SourceUniqueID:   URLHash(link),
			SourceURL:        link,
			CoverImageURL:    firstImage(append([]string{item.Image, item.ImageURL, item.Thumbnail}, item.Images...)...),
			Title:            title,
			Summary:          summary,
			Content:          content,
			Author:           CleanText(item.Author, 80),
			Category:         category,
			OriginalCategory: categoryRaw,
			ContentType:      NormalizeContentType(category),
			PublishedAt:      ParsePublishedAt(item.PubDate),
			CollectedAt:      time.Now().UTC(),
			Region:           "CN",
			Language:         "zh-CN",
			CopyrightMode:    copyrightMode,
			RawData:          truncateRaw(body, 1200),
		}
		normalized.SecurityCodes = ExtractSecurityCodes(title + " " + summary)
		result = append(result, normalized)
	}
	return result, nil
}

func firstImage(values ...string) string {
	for _, value := range values {
		if normalized := strings.TrimSpace(value); normalized != "" {
			return normalized
		}
	}
	return ""
}

func truncateRaw(raw []byte, max int) string {
	text := strings.TrimSpace(string(raw))
	if max > 0 && len([]rune(text)) > max {
		r := []rune(text)
		text = string(r[:max])
	}
	return text
}
