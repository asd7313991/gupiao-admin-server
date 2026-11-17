package customerservice

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"unicode"
)

func ValidateURL(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) > 4096 || strings.ContainsFunc(value, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) }) {
		return "", fmt.Errorf("客服链接过长或包含空白字符")
	}
	link, err := url.Parse(value)
	if err != nil || link.Hostname() == "" || link.User != nil || (link.Scheme != "https" && link.Scheme != "http") {
		return "", fmt.Errorf("请填写完整的 HTTP/HTTPS 访客聊天链接，不能包含账号密码")
	}
	if strings.EqualFold(link.Hostname(), "service.example.com") {
		return "", fmt.Errorf("不能使用示例地址，请填写米多客提供的真实访客链接")
	}
	return value, nil
}

func parseConfig(content string) (map[string]json.RawMessage, map[string]json.RawMessage, error) {
	var root map[string]json.RawMessage
	if err := json.Unmarshal([]byte(content), &root); err != nil || root == nil {
		return nil, nil, fmt.Errorf("系统设置格式错误")
	}
	links := make(map[string]json.RawMessage)
	if raw, ok := root["links"]; ok && string(raw) != "null" {
		if err := json.Unmarshal(raw, &links); err != nil {
			return nil, nil, fmt.Errorf("系统服务链接配置格式错误")
		}
	}
	if links == nil {
		links = make(map[string]json.RawMessage)
	}
	return root, links, nil
}

func FromConfig(content string) (string, error) {
	_, links, err := parseConfig(content)
	if err != nil {
		return "", err
	}
	var value string
	if raw, ok := links["customerService"]; ok {
		if err := json.Unmarshal(raw, &value); err != nil {
			return "", fmt.Errorf("客服链接配置格式错误")
		}
	}
	// 旧版种子地址不代表客服已配置。
	if strings.TrimSpace(value) == "https://service.example.com" {
		return "", nil
	}
	return ValidateURL(value)
}

func WithURL(content, value string) (string, error) {
	value, err := ValidateURL(value)
	if err != nil {
		return "", err
	}
	root, links, err := parseConfig(content)
	if err != nil {
		return "", err
	}
	links["customerService"], err = json.Marshal(value)
	if err != nil {
		return "", err
	}
	root["links"], err = json.Marshal(links)
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(root)
	return string(result), err
}

// 系统设置页面不再编辑客服地址，避免旧页面快照覆盖客服设置页刚保存的地址。
func PreserveURL(incoming, stored string) (string, error) {
	root, links, err := parseConfig(incoming)
	if err != nil {
		return "", err
	}
	_, currentLinks, err := parseConfig(stored)
	if err != nil {
		return "", err
	}
	delete(links, "customerService")
	if value, ok := currentLinks["customerService"]; ok {
		links["customerService"] = value
	}
	root["links"], err = json.Marshal(links)
	if err != nil {
		return "", err
	}
	result, err := json.Marshal(root)
	return string(result), err
}
