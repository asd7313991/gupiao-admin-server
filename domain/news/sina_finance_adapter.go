package news

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"golang.org/x/net/html"
)

type SinaFinanceAdapter struct{}

func (adapter SinaFinanceAdapter) Key() string {
	return "sina_finance"
}

func (adapter SinaFinanceAdapter) Fetch(ctx context.Context, source SourceReader, cfg SourceConfig) ([]NormalizedNews, error) {
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
	request.Header.Set("User-Agent", "stock-news-collector/1.0 (+compliance)")
	request.Header.Set("Accept", "text/html,application/xhtml+xml")

	client := &http.Client{Timeout: timeout}
	resp, err := client.Do(request)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrRetryable, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 500 {
		return nil, fmt.Errorf("%w: upstream %d", ErrRetryable, resp.StatusCode)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("unexpected status: %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit(cfg)))
	if err != nil {
		return nil, fmt.Errorf("%w: read body failed: %v", ErrRetryable, err)
	}
	root, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return nil, fmt.Errorf("parse html failed: %w", err)
	}

	maxItems := cfg.MaxItems
	if maxItems <= 0 {
		maxItems = 30
	}
	category := cfg.Category
	if category == "" {
		category = CategoryFinance
	}
	seen := make(map[string]struct{})
	result := make([]NormalizedNews, 0, maxItems)
	walkHTML(root, func(node *html.Node) {
		if len(result) >= maxItems || node.Type != html.ElementNode || node.Data != "a" {
			return
		}
		href := attr(node, "href")
		link, ok := normalizeSinaNewsURL(href)
		if !ok {
			return
		}
		title := nodeText(node)
		if len([]rune(title)) < 6 || title == "新浪财经" {
			return
		}
		key := URLHash(link)
		if _, exists := seen[key]; exists {
			return
		}
		seen[key] = struct{}{}
		metadata := fetchSinaArticleMetadata(ctx, client, link, timeout)
		coverImageURL := firstImageIn(node, link)
		if coverImageURL == "" {
			coverImageURL = metadata.CoverImageURL
		}
		summary := metadata.Summary
		if summary == "" {
			summary = title
		}
		result = append(result, NormalizedNews{
			SourceName:     source.GetName(),
			SourceUniqueID: key,
			SourceURL:      link,
			CoverImageURL:  coverImageURL,
			Title:          title,
			Summary:        summary,
			Content:        summary,
			Category:       category,
			ContentType:    ContentTypeArticle,
			PublishedAt:    time.Now().UTC(),
			CollectedAt:    time.Now().UTC(),
			Region:         "CN",
			Language:       "zh-CN",
			CopyrightMode:  CopyrightModeMetadataOnly,
			RawData:        truncateRaw(body, 1200),
		})
	})
	return result, nil
}

func responseLimit(cfg SourceConfig) int64 {
	if cfg.MaxResponseBytes > 0 {
		return cfg.MaxResponseBytes
	}
	return 4 * 1024 * 1024
}

func normalizeSinaNewsURL(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || parsed.Host == "" {
		return "", false
	}
	if parsed.Host != "finance.sina.com.cn" && parsed.Host != "finance.sina.cn" {
		return "", false
	}
	if !strings.Contains(parsed.Path, "/doc-") && !strings.Contains(parsed.Path, "/7x24/") && !strings.Contains(parsed.Path, "/roll/") {
		return "", false
	}
	return parsed.String(), true
}

func walkHTML(node *html.Node, visit func(*html.Node)) {
	visit(node)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		walkHTML(child, visit)
	}
}

func attr(node *html.Node, name string) string {
	for _, item := range node.Attr {
		if item.Key == name {
			return item.Val
		}
	}
	return ""
}

func nodeText(node *html.Node) string {
	if node.Type == html.TextNode {
		return strings.Join(strings.Fields(html.UnescapeString(node.Data)), " ")
	}
	parts := make([]string, 0, 4)
	for child := node.FirstChild; child != nil; child = child.NextSibling {
		if child.Type == html.ElementNode && (child.Data == "script" || child.Data == "style") {
			continue
		}
		if text := nodeText(child); text != "" {
			parts = append(parts, text)
		}
	}
	return strings.Join(parts, " ")
}

func firstImageIn(node *html.Node, base string) string {
	var image string
	walkHTML(node, func(child *html.Node) {
		if image != "" || child.Type != html.ElementNode || child.Data != "img" {
			return
		}
		for _, name := range []string{"src", "data-src", "data-original"} {
			if value := strings.TrimSpace(attr(child, name)); value != "" {
				if parsed, err := url.Parse(value); err == nil {
					if !parsed.IsAbs() {
						if resolved, resolveErr := url.Parse(base); resolveErr == nil {
							value = resolved.ResolveReference(parsed).String()
						}
					}
					if strings.HasPrefix(value, "http://") || strings.HasPrefix(value, "https://") {
						image = value
					}
				}
			}
			if image != "" {
				return
			}
		}
	})
	return image
}

type sinaArticleMetadata struct {
	CoverImageURL string
	Summary       string
}

func fetchSinaArticleMetadata(ctx context.Context, client *http.Client, link string, timeout time.Duration) sinaArticleMetadata {
	requestCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodGet, link, nil)
	if err != nil {
		return sinaArticleMetadata{}
	}
	request.Header.Set("User-Agent", "stock-news-collector/1.0 (+compliance)")
	response, err := client.Do(request)
	if err != nil || response.StatusCode != http.StatusOK {
		if response != nil {
			response.Body.Close()
		}
		return sinaArticleMetadata{}
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 512*1024))
	if err != nil {
		return sinaArticleMetadata{}
	}
	root, err := html.Parse(strings.NewReader(string(body)))
	if err != nil {
		return sinaArticleMetadata{}
	}
	metadata := sinaArticleMetadata{}
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
			metadata.Summary = CleanText(value, 90)
		}
	})
	return metadata
}
