package verificationmaterial

import (
	"bytes"
	"image"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckFace(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "face.jpg")
	var data bytes.Buffer
	if err := jpeg.Encode(&data, image.NewRGBA(image.Rect(0, 0, 160, 160)), nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data.Bytes(), 0600); err != nil {
		t.Fatal(err)
	}
	if err := CheckFace(root, path); err != nil {
		t.Fatal(err)
	}
	for _, invalid := range []string{"", "face.jpg", filepath.Join(t.TempDir(), "face.jpg"), filepath.Join(root, "missing.jpg"), root} {
		if err := CheckFace(root, invalid); err == nil {
			t.Fatalf("应拒绝无效材料：%s", invalid)
		}
	}
	for _, invalid := range [][]byte{nil, []byte("not a photo"), data.Bytes()[:len(data.Bytes())/2], make([]byte, 2*1024*1024+1)} {
		if err := os.WriteFile(path, invalid, 0600); err != nil {
			t.Fatal(err)
		}
		if err := CheckFace(root, path); err == nil {
			t.Fatal("应拒绝空白、损坏或超过2MB的文件")
		}
	}
}
