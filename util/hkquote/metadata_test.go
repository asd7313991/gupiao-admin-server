package hkquote

import (
	"testing"
	"time"
)

func TestMetadataValidityDoesNotRefreshSourceTime(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	valid := Metadata{LotSize: 2000, Currency: "HKD", Kind: "GP", At: now.Add(-24 * time.Hour).Unix()}
	if !valid.ValidAt(now) || valid.ValidAt(now.Add(time.Second)) {
		t.Fatal("证券资料仅允许24小时以内，不得延长源时间")
	}
	valid.At = now.Add(60 * time.Second).Unix()
	if !valid.ValidAt(now) {
		t.Fatal("保留60秒时钟容差")
	}
	valid.At++
	if valid.ValidAt(now) {
		t.Fatal("不得缓存未来证券资料")
	}
	valid.At, valid.LotSize = now.Unix(), 0
	if valid.ValidAt(now) {
		t.Fatal("不得猜测缺失的每手股数")
	}
	if _, err := ParseTencentMetadata([]string{"short"}); err == nil {
		t.Fatal("资料字段不足必须报错")
	}
}
