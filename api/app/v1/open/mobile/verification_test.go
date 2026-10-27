package mobile

import (
	"testing"

	"api-server/db/pgdb/system"
)

func TestValidateVerificationMaterials(t *testing.T) {
	for _, tc := range []struct {
		name     string
		customer system.Customer
		message  string
	}{
		{"未上传材料", system.Customer{}, "请先上传身份证正反面"},
		{"缺少国徽面", system.Customer{IDCardFront: "front.jpg", VerificationVideo: "video.mp4"}, "请先上传身份证正反面"},
		{"缺少人像面", system.Customer{IDCardBack: "back.jpg", VerificationVideo: "video.mp4"}, "请先上传身份证正反面"},
		{"缺少照片", system.Customer{IDCardFront: "front.jpg", IDCardBack: "back.jpg"}, "请先拍摄并上传正脸照片"},
		{"空白照片", system.Customer{IDCardFront: "front.jpg", IDCardBack: "back.jpg", VerificationFace: " "}, "请先拍摄并上传正脸照片"},
		{"历史视频不能替代照片", system.Customer{IDCardFront: "front.jpg", IDCardBack: "back.jpg", VerificationVideo: "video.mp4"}, "请先拍摄并上传正脸照片"},
		{"无需新视频", system.Customer{IDCardFront: "front.jpg", IDCardBack: "back.jpg", VerificationFace: "face.jpg"}, ""},
		{"保留历史视频", system.Customer{IDCardFront: "front.jpg", IDCardBack: "back.jpg", VerificationFace: "face.jpg", VerificationVideo: "video.mp4"}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateVerificationMaterials(tc.customer)
			if tc.message == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || err.Error() != tc.message {
				t.Fatalf("预期错误 %q，实际 %v", tc.message, err)
			}
		})
	}
}
