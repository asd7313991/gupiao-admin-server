package mobile

import "testing"

func TestMobileArticleTypesAllowBeginnerGuideSections(t *testing.T) {
	for _, articleType := range []string{
		"新手必看",
		"交易规则（A股）",
		"交易规则（港股）",
		"费用收取（A股）",
		"费用收取（港股）",
		"常见问题",
	} {
		if _, ok := mobileArticleTypes[articleType]; !ok {
			t.Errorf("mobile article type %q must be available to the beginner guide", articleType)
		}
	}
}
