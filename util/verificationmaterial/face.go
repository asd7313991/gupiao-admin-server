package verificationmaterial

import (
	"fmt"
	"image/jpeg"
	"io"
	"os"
	"path/filepath"
	"strings"
)

func CheckFace(root, path string) error {
	if !filepath.IsAbs(path) {
		return fmt.Errorf("请先拍摄并上传正脸照片")
	}
	directory, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("认证材料目录不可用")
	}
	if !strings.HasPrefix(filepath.Clean(path), filepath.Clean(directory)+string(os.PathSeparator)) {
		return fmt.Errorf("正脸照片路径无效，请重新拍摄")
	}
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("无法读取正脸照片，请重新拍摄上传或联系管理员")
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() == 0 || info.Size() > 2*1024*1024 {
		return fmt.Errorf("正脸照片文件无效，请重新拍摄")
	}
	config, err := jpeg.DecodeConfig(file)
	if err != nil || config.Width < 160 || config.Height < 160 || config.Width > 4096 || config.Height > 4096 || config.Width*config.Height > 12_000_000 {
		return fmt.Errorf("正脸照片内容或尺寸无效，请重新拍摄")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("无法读取正脸照片，请重新拍摄")
	}
	if _, err := jpeg.Decode(file); err != nil {
		return fmt.Errorf("正脸照片损坏，请重新拍摄")
	}
	return nil
}
