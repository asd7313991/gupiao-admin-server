package mobile

import (
	"bytes"
	"image"
	"image/jpeg"
	"image/png"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

func TestNormalizeVerificationFace(t *testing.T) {
	var jpegData, pngData bytes.Buffer
	if err := jpeg.Encode(&jpegData, image.NewRGBA(image.Rect(0, 0, 160, 200)), nil); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&pngData, image.NewRGBA(image.Rect(0, 0, 160, 200))); err != nil {
		t.Fatal(err)
	}
	marker := []byte("TEST_PRIVATE_METADATA")
	segment := append([]byte{0xff, 0xe1, 0, byte(len(marker) + 2)}, marker...)
	withMetadata := append(append(append([]byte{}, jpegData.Bytes()[:2]...), segment...), jpegData.Bytes()[2:]...)
	for _, input := range [][]byte{jpegData.Bytes(), pngData.Bytes(), withMetadata} {
		output, err := normalizeVerificationFace(input)
		if err != nil {
			t.Fatal(err)
		}
		config, format, err := image.DecodeConfig(bytes.NewReader(output))
		if err != nil || format != "jpeg" || config.Width != 160 || config.Height != 200 {
			t.Fatalf("照片应保持尺寸并转为JPEG：%+v %s %v", config, format, err)
		}
		if bytes.Contains(output, marker) {
			t.Fatal("转换后不应保留原始元数据")
		}
	}
	for _, size := range []image.Point{{159, 200}, {200, 159}} {
		var data bytes.Buffer
		if err := jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, size.X, size.Y)), nil); err != nil {
			t.Fatal(err)
		}
		if _, err := normalizeVerificationFace(data.Bytes()); err == nil {
			t.Fatal("应拒绝尺寸过小的照片")
		}
	}
	for _, input := range [][]byte{nil, []byte("not an image"), jpegData.Bytes()[:50]} {
		if _, err := normalizeVerificationFace(input); err == nil {
			t.Fatal("应拒绝无效或损坏的照片")
		}
	}
	for _, size := range []image.Point{{4097, 160}, {160, 4097}, {4000, 4000}} {
		data := append([]byte(nil), jpegData.Bytes()...)
		index := bytes.Index(data, []byte{0xff, 0xc0})
		if index < 0 {
			t.Fatal("测试JPEG缺少尺寸段")
		}
		data[index+5], data[index+6] = byte(size.Y>>8), byte(size.Y)
		data[index+7], data[index+8] = byte(size.X>>8), byte(size.X)
		if _, err := normalizeVerificationFace(data); err == nil {
			t.Fatal("应在解码图片前拒绝过大的尺寸或像素总数")
		}
	}
}

func TestLegacyFaceEndpointsDisabled(t *testing.T) {
	for _, handler := range []gin.HandlerFunc{StartFaceVerification, ConfirmFaceVerification} {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		handler(context)
		body := recorder.Body.String()
		if !bytes.Contains([]byte(body), []byte("人工审核")) || bytes.Contains([]byte(body), []byte(`"passed":true`)) {
			t.Fatalf("旧入口必须明确提示人工审核，不得自动认证通过：%s", body)
		}
	}
}
