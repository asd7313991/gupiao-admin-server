package news

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"

	"golang.org/x/net/html"
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

	client := &http.Client{Timeout: timeout}
	response, err := client.Do(request)
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
		sourceURL := normalizeYicaiURL(item.URL)
		metadata := fetchYicaiArticleMetadata(ctx, client, sourceURL, timeout)

		summary := CleanText(item.NewsNotes, 600)
		if summary == "" {
			summary = metadata.Summary
		}
		content := metadata.Content
		if content == "" {
			content = summary
		}
		coverImageURL := firstImage(strings.TrimSpace(item.OriginPic), strings.TrimSpace(item.NewsThumbs), metadata.CoverImageURL)

		copyrightMode := CopyrightModeMetadataOnly
		if summary != "" || content != "" {
			copyrightMode = CopyrightModeExcerpt
		}
		publishedAt := parseYicaiTime(item.CreateDate)
		if publishedAt.IsZero() {
			publishedAt = time.Now().UTC()
		}
		result = append(result, NormalizedNews{
			SourceName:       defaultString(CleanText(item.NewsSource, 100), source.GetName()),
			SourceUniqueID:   fmt.Sprintf("yicai-%d", item.NewsID),
			SourceURL:        sourceURL,
			CoverImageURL:    coverImageURL,
			Title:            title,
			Summary:          summary,
			Content:          content,
			Author:           CleanText(item.NewsAuthor, 120),
			Category:         itemCategory,
			OriginalCategory: CleanText(item.ChannelName, 120),
			ContentType:      ContentTypeArticle,
			PublishedAt:      publishedAt,
			CollectedAt:      time.Now().UTC(),
			Region:           "CN",
			Language:         "zh-CN",
			CopyrightMode:    copyrightMode,
			RawData:          truncateRaw(body, 1200),
		})
	}
	return result, nil
}

type yicaiArticleMetadata struct {
	Summary       string
	Content       string
	CoverImageURL string
}

var yicaiSharePrefixPattern = regexp.MustCompile(`^打开微信[，,].*?朋友圈。`)
var yicaiLeadMetaPattern = regexp.MustCompile(`^第一财经\s*\d{4}-\d{2}-\d{2}\s+\d{2}:\d{2}:\d{2}\s*`)
var yicaiSubscriptionTailPattern = regexp.MustCompile(`(?:【\s*推荐订阅\s*】|\[\s*推荐订阅\s*\])[\s\S]*$`)

func normalizeYicaiURL(raw string) string {
	base, _ := url.Parse("https://www.yicai.com")
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed == nil {
		return "https://www.yicai.com"
	}
	if parsed.IsAbs() {
		return parsed.String()
	}
	if base == nil {
		return strings.TrimSpace(raw)
	}
	return base.ResolveReference(parsed).String()
}

func fetchYicaiArticleMetadata(ctx context.Context, client *http.Client, link string, timeout time.Duration) yicaiArticleMetadata {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, link, nil)
	if err != nil {
		return yicaiArticleMetadata{}
	}
	request.Header.Set("User-Agent", "stock-news-collector/1.0")
	request.Header.Set("Accept", "text/html,application/xhtml+xml")
	request.Header.Set("Referer", "https://www.yicai.com/")

	response, err := client.Do(request)
	if err != nil || response.StatusCode != http.StatusOK {
		if response != nil {
			response.Body.Close()
		}
		return yicaiArticleMetadata{}
	}
	defer response.Body.Close()

	body, err := io.ReadAll(io.LimitReader(response.Body, 2*1024*1024))
	if err != nil {
		return yicaiArticleMetadata{}
	}
	root, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return yicaiArticleMetadata{}
	}

	metadata := yicaiArticleMetadata{}
	walkHTML(root, func(node *html.Node) {
		if node.Type != html.ElementNode || node.Data != "meta" {
			return
		}
		property := strings.ToLower(attr(node, "property"))
		name := strings.ToLower(attr(node, "name"))
		value := strings.TrimSpace(attr(node, "content"))
		if (property == "og:image" || name == "og:image") && metadata.CoverImageURL == "" {
			if strings.HasPrefix(value, "//") {
				value = "https:" + value
			}
			if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
				metadata.CoverImageURL = value
			}
		}
		if (name == "description" || property == "og:description") && metadata.Summary == "" {
			metadata.Summary = cleanYicaiNoise(value, 300)
		}
	})

	paragraphs := make([]string, 0, 16)
	seen := make(map[string]struct{})
	walkHTML(root, func(node *html.Node) {
		if node.Type != html.ElementNode || node.Data != "p" {
			return
		}
		text := cleanYicaiNoise(nodeText(node), 600)
		if len([]rune(text)) < 18 {
			return
		}
		if isYicaiNoiseParagraph(text) {
			return
		}
		if _, exists := seen[text]; exists {
			return
		}
		seen[text] = struct{}{}
		paragraphs = append(paragraphs, text)
	})
	if len(paragraphs) > 0 {
		metadata.Content = cleanYicaiNoise(strings.Join(paragraphs, "\n\n"), 7000)
		if metadata.Summary == "" {
			metadata.Summary = cleanYicaiNoise(paragraphs[0], 300)
		}
	}
	if metadata.Summary != "" {
		metadata.Summary = cleanYicaiNoise(metadata.Summary, 300)
	}
	return metadata
}

func cleanYicaiNoise(input string, maxLen int) string {
	text := CleanText(input, maxLen)
	if text == "" {
		return ""
	}
	text = yicaiSharePrefixPattern.ReplaceAllString(text, "")
	text = yicaiLeadMetaPattern.ReplaceAllString(text, "")
	text = yicaiSubscriptionTailPattern.ReplaceAllString(text, "")
	text = strings.TrimSpace(text)
	return CleanText(text, maxLen)
}

func isYicaiNoiseParagraph(input string) bool {
	plain := strings.ReplaceAll(strings.TrimSpace(input), " ", "")
	if plain == "" {
		return true
	}
	noiseKeywords := []string{"推荐订阅", "公告臻选", "告别信息过载", "捕捉每天公告中的"}
	for _, keyword := range noiseKeywords {
		if strings.Contains(plain, keyword) {
			return true
		}
	}
	return false
}

func parseYicaiTime(value string) time.Time {
	parsed, err := time.ParseInLocation("2006-01-02T15:04:05", strings.TrimSpace(value), time.Local)
	if err != nil {
		return time.Time{}
	}
	return parsed
}
