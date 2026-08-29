package news

import (
	"strings"
	"testing"
	"time"
)

func TestNormalizeCategory(t *testing.T) {
	if got := NormalizeCategory("宏观经济", nil); got != CategoryEconomy {
		t.Fatalf("expected ECONOMY, got %s", got)
	}
	if got := NormalizeCategory("上市公司公告", nil); got != CategoryListedCompany {
		t.Fatalf("expected LISTED_COMPANY, got %s", got)
	}
	custom := map[string]string{"央行": CategoryCentralBank}
	if got := NormalizeCategory("央行逆回购", custom); got != CategoryCentralBank {
		t.Fatalf("expected CENTRAL_BANK, got %s", got)
	}
}

func TestNormalizeURL(t *testing.T) {
	normalized, err := NormalizeURL("https://example.com/path?a=1&utm_source=xx&ref=abc#frag")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.Contains(normalized, "utm_source") || strings.Contains(normalized, "#") || strings.Contains(normalized, "ref=") {
		t.Fatalf("unexpected tracking params: %s", normalized)
	}
}

func TestContentHashStable(t *testing.T) {
	now := time.Unix(1787570400, 0)
	h1 := ContentHash("政策发布", "国务院", now)
	h2 := ContentHash("政策发布", "国务院", now)
	if h1 != h2 {
		t.Fatalf("expected same hash with same input")
	}
	h3 := ContentHash("政策发布", "国务院", now.Add(11*time.Minute))
	if h1 == h3 {
		t.Fatalf("expected different hash across buckets")
	}
}

func TestCleanTextAndXSS(t *testing.T) {
	clean := CleanText("<script>alert(1)</script><p>  央行  逆回购 </p>", 200)
	if strings.Contains(clean, "script") {
		t.Fatalf("xss tag should be removed: %s", clean)
	}
	if !strings.Contains(clean, "央行 逆回购") {
		t.Fatalf("unexpected clean text: %s", clean)
	}
}

func TestExtractSecurityCodes(t *testing.T) {
	codes := ExtractSecurityCodes("关注 SH600000、sz000001 和 300750")
	if len(codes) != 3 {
		t.Fatalf("expected 3 codes, got %d", len(codes))
	}
}

func TestParsePublishedAt(t *testing.T) {
	timeValue := ParsePublishedAt("2026-08-24 09:30:00")
	if timeValue.IsZero() {
		t.Fatalf("expected parsed time")
	}
}
