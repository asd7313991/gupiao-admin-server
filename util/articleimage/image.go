package articleimage

import (
	"bytes"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	_ "golang.org/x/image/webp"
)

const MaxBytes = 5 * 1024 * 1024
const URLPrefix = "/api/v1/open/mobile/article-images/"

var Directory = filepath.Join("static", "article-images")
var filenamePattern = regexp.MustCompile(`^photo-[0-9a-f]{24}\.(jpg|png|webp)$`)

func ValidName(name string) bool {
	return filenamePattern.MatchString(name)
}

func Check(data []byte) (string, error) {
	if len(data) == 0 || len(data) > MaxBytes {
		return "", fmt.Errorf("请选择不超过 5MB 的照片")
	}
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("照片仅支持有效的 JPEG、PNG 或 WebP 图片")
	}
	if config.Width <= 0 || config.Height <= 0 || config.Width > 12000 || config.Height > 12000 || int64(config.Width)*int64(config.Height) > 24_000_000 {
		return "", fmt.Errorf("照片尺寸过大，最长边不能超过12000像素，总像素不能超过2400万")
	}
	extension := map[string]string{"jpeg": ".jpg", "png": ".png", "webp": ".webp"}[format]
	if extension == "" {
		return "", fmt.Errorf("照片仅支持 JPEG、PNG 或 WebP 格式")
	}
	if _, _, err := image.Decode(bytes.NewReader(data)); err != nil {
		return "", fmt.Errorf("照片已损坏，请重新上传")
	}
	return extension, nil
}

func CheckReferences(urls []string) error {
	for _, url := range urls {
		name := strings.TrimPrefix(url, URLPrefix)
		if !strings.HasPrefix(url, URLPrefix) || !ValidName(name) {
			return fmt.Errorf("照片地址无效，请使用上传按钮重新上传")
		}
		info, err := os.Stat(filepath.Join(Directory, name))
		if err != nil || !info.Mode().IsRegular() {
			return fmt.Errorf("照片文件不存在或不可读取，请重新上传")
		}
	}
	return nil
}
