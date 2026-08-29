package news

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/url"
	"regexp"
	"sort"
	"strings"
	"time"
)

var htmlTagPattern = regexp.MustCompile(`(?is)<[^>]*>`)

var securityCodePattern = regexp.MustCompile(`\b(?:SH|SZ|BJ)?\d{6}\b`)

type categoryRule struct {
	key   string
	value string
}

var defaultCategoryRules = []categoryRule{
	{key: "7×24", value: CategoryFlash},
	{key: "7x24", value: CategoryFlash},
	{key: "快讯", value: CategoryFlash},
	{key: "上市公司", value: CategoryListedCompany},
	{key: "公司", value: CategoryListedCompany},
	{key: "央行", value: CategoryCentralBank},
	{key: "财经", value: CategoryFinance},
	{key: "经济", value: CategoryEconomy},
	{key: "宏观", value: CategoryEconomy},
	{key: "商品", value: CategoryCommodity},
	{key: "a股", value: CategoryAShare},
	{key: "港股", value: CategoryHKStock},
	{key: "美股", value: CategoryUSStock},
	{key: "行业", value: CategoryIndustry},
	{key: "公告", value: CategoryAnnouncement},
	{key: "政策", value: CategoryPolicy},
}

func NormalizeCategory(original string, custom map[string]string) string {
	plain := strings.ToLower(strings.TrimSpace(original))
	if plain == "" {
		return CategoryOther
	}

	if custom != nil {
		for key, value := range custom {
			if strings.Contains(plain, strings.ToLower(strings.TrimSpace(key))) {
				if value != "" {
					return value
				}
			}
		}
	}

	for _, rule := range defaultCategoryRules {
		if strings.Contains(plain, rule.key) {
			return rule.value
		}
	}
	return CategoryOther
}

func NormalizeContentType(category string) string {
	switch category {
	case CategoryFlash:
		return ContentTypeFlash
	case CategoryAnnouncement:
		return ContentTypeAnnouncement
	default:
		return ContentTypeArticle
	}
}

func CleanText(input string, maxLen int) string {
	plain := strings.TrimSpace(input)
	if plain == "" {
		return ""
	}
	plain = htmlTagPattern.ReplaceAllString(plain, " ")
	plain = strings.ReplaceAll(plain, "&nbsp;", " ")
	plain = strings.ReplaceAll(plain, "\t", " ")
	plain = strings.ReplaceAll(plain, "\r", "\n")
	plain = collapseWhitespace(plain)
	if maxLen > 0 && len([]rune(plain)) > maxLen {
		runes := []rune(plain)
		plain = string(runes[:maxLen])
	}
	return strings.TrimSpace(plain)
}

func collapseWhitespace(input string) string {
	parts := strings.Fields(input)
	return strings.Join(parts, " ")
}

func NormalizeURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return "", fmt.Errorf("unsupported url scheme: %s", u.Scheme)
	}
	u.Fragment = ""
	query := u.Query()
	for key := range query {
		k := strings.ToLower(strings.TrimSpace(key))
		if strings.HasPrefix(k, "utm_") || strings.HasPrefix(k, "spm") || k == "from" || k == "source" || k == "ref" {
			query.Del(key)
		}
	}
	u.RawQuery = query.Encode()
	u.Host = strings.ToLower(u.Host)
	return u.String(), nil
}

func URLHash(raw string) string {
	h := sha256.Sum256([]byte(strings.TrimSpace(raw)))
	return hex.EncodeToString(h[:])
}

func ContentHash(title, source string, publishedAt time.Time) string {
	bucket := publishedAt.UTC().Unix() / 600
	base := strings.ToLower(strings.TrimSpace(title)) + "|" + strings.ToLower(strings.TrimSpace(source)) + "|" + fmt.Sprint(bucket)
	h := sha256.Sum256([]byte(base))
	return hex.EncodeToString(h[:])
}

func ExtractSecurityCodes(text string) []string {
	matches := securityCodePattern.FindAllString(strings.ToUpper(text), -1)
	if len(matches) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(matches))
	result := make([]string, 0, len(matches))
	for _, value := range matches {
		normalized := strings.TrimPrefix(strings.TrimPrefix(strings.TrimPrefix(value, "SH"), "SZ"), "BJ")
		if len(normalized) != 6 {
			continue
		}
		if _, exists := seen[normalized]; exists {
			continue
		}
		seen[normalized] = struct{}{}
		result = append(result, normalized)
	}
	sort.Strings(result)
	return result
}

func ParsePublishedAt(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Now().UTC()
	}
	formats := []string{
		time.RFC3339,
		"2006-01-02 15:04:05",
		"2006-01-02 15:04",
		"2006-01-02",
		"2006/01/02 15:04:05",
		"2006/01/02",
	}
	for _, format := range formats {
		if parsed, err := time.ParseInLocation(format, raw, time.Local); err == nil {
			return parsed.UTC()
		}
	}
	return time.Now().UTC()
}
