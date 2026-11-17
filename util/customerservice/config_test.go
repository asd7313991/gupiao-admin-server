package customerservice

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestValidateURL(t *testing.T) {
	for _, value := range []string{"", " https://chat.company.test/visitor?id=123&lang=zh#chat ", "http://localhost:8080/chat"} {
		got, err := ValidateURL(value)
		if err != nil || got != strings.TrimSpace(value) {
			t.Fatalf("有效链接未保留：%q, %v", got, err)
		}
	}
	for _, value := range []string{
		"javascript:alert(1)", "data:text/html,test", "file:///tmp/chat",
		"//chat.company.test", "/chat", "https://", "https://user:pass@chat.company.test",
		"https://chat.company.test/\npath", "https://chat.company.test/\x00path",
		"https://chat.company.test/a b", "https://service.example.com",
		"https://chat.company.test/" + strings.Repeat("a", 4096),
	} {
		if _, err := ValidateURL(value); err == nil {
			t.Errorf("无效链接被接受：%q", value)
		}
	}
}

func TestWithURLPreservesOtherSettings(t *testing.T) {
	original := `{"trade":{"fee":0.01},"custom":[1,2],"links":{"customerService":"https://old.test","hkdRate":0.89,"extra":{"enabled":true}}}`
	got, err := WithURL(original, "https://chat.company.test/visitor?site=1#chat")
	if err != nil {
		t.Fatal(err)
	}
	var expected, actual map[string]any
	if err := json.Unmarshal([]byte(original), &expected); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(got), &actual); err != nil {
		t.Fatal(err)
	}
	expected["links"].(map[string]any)["customerService"] = "https://chat.company.test/visitor?site=1#chat"
	if !reflect.DeepEqual(expected, actual) {
		t.Fatal("修改客服链接改变了其他设置")
	}
	got, err = WithURL(got, "")
	if err != nil {
		t.Fatal(err)
	}
	if link, err := FromConfig(got); err != nil || link != "" {
		t.Fatalf("清空客服配置失败：%q, %v", link, err)
	}
	for _, content := range []string{`{}`, `{"links":null}`, "{ \"links\": null }"} {
		got, err := WithURL(content, "https://chat.company.test")
		if err != nil {
			t.Fatal(err)
		}
		if link, err := FromConfig(got); err != nil || link != "https://chat.company.test" {
			t.Fatalf("缺少旧客服字段时无法保存：%q, %v", link, err)
		}
	}
	if link, err := FromConfig(`{"links":{"customerService":"https://service.example.com"}}`); err != nil || link != "" {
		t.Fatal("旧版示例地址不应提供给访客")
	}
	for _, content := range []string{"null", "[]", "{", `{"links":[]}`} {
		if _, err := WithURL(content, "https://chat.company.test"); err == nil {
			t.Errorf("损坏设置未报错：%s", content)
		}
	}
}

func TestPreserveURLRejectsStaleSystemSnapshot(t *testing.T) {
	incoming := `{"trade":{"fee":2},"links":{"customerService":"https://stale.test","aQuote":"new"}}`
	for _, stored := range []string{
		`{"links":{"customerService":"https://current.test"}}`,
		`{"links":{"customerService":""}}`,
		`{"links":{}}`,
	} {
		got, err := PreserveURL(incoming, stored)
		if err != nil {
			t.Fatal(err)
		}
		link, err := FromConfig(got)
		if err != nil {
			t.Fatal(err)
		}
		expected, err := FromConfig(stored)
		if err != nil {
			t.Fatal(err)
		}
		if link != expected || !strings.Contains(got, `"fee":2`) || !strings.Contains(got, `"aQuote":"new"`) {
			t.Fatalf("旧快照覆盖了客服地址或其他新设置未保存：%s", got)
		}
	}
}
