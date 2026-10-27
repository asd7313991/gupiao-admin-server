package mobile

import (
	"bytes"
	"fmt"
	"image"
	"image/jpeg"
	_ "image/png"
)

func normalizeVerificationFace(data []byte) ([]byte, error) {
	config, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "jpeg" && format != "png") {
		return nil, fmt.Errorf("正脸照片必须是有效的JPEG或PNG图片")
	}
	if config.Width < 160 || config.Height < 160 || config.Width > 4096 || config.Height > 4096 || config.Width*config.Height > 12_000_000 {
		return nil, fmt.Errorf("正脸照片尺寸应不小于160像素且不超过4096像素，总像素不超过1200万")
	}
	photo, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("正脸照片内容无法读取，请重新拍摄")
	}
	var output bytes.Buffer
	if err := jpeg.Encode(&output, photo, &jpeg.Options{Quality: 90}); err != nil {
		return nil, fmt.Errorf("正脸照片转换失败，请重新拍摄")
	}
	if output.Len() > 2*1024*1024 {
		return nil, fmt.Errorf("正脸照片转换后超过2MB，请降低拍摄分辨率后重试")
	}
	return output.Bytes(), nil
}
