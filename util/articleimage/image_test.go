package articleimage

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

func TestCheck(t *testing.T) {
	picture := image.NewRGBA(image.Rect(0, 0, 2, 3))
	var jpg, pngData bytes.Buffer
	if err := jpeg.Encode(&jpg, picture, nil); err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(&pngData, picture); err != nil {
		t.Fatal(err)
	}
	webpData, err := base64.StdEncoding.DecodeString("UklGRiQAAABXRUJQVlA4IBgAAAAwAQCdASoBAAEAAUAmJaQAA3AA/v02aAA=")
	if err != nil {
		t.Fatal(err)
	}
	exactLimit := make([]byte, MaxBytes)
	copy(exactLimit, pngData.Bytes())
	for _, tc := range []struct {
		name      string
		data      []byte
		extension string
	}{
		{"jpeg", jpg.Bytes(), ".jpg"},
		{"png", pngData.Bytes(), ".png"},
		{"webp", webpData, ".webp"},
		{"exact size limit", exactLimit, ".png"},
		{"empty", nil, ""},
		{"too large", make([]byte, MaxBytes+1), ""},
		{"html", []byte("<script>alert(1)</script>"), ""},
		{"svg", []byte(`<svg xmlns="http://www.w3.org/2000/svg"/>`), ""},
		{"truncated png", pngData.Bytes()[:40], ""},
		{"truncated jpeg", jpg.Bytes()[:len(jpg.Bytes())/2], ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			extension, err := Check(tc.data)
			if extension != tc.extension || (err == nil) != (tc.extension != "") {
				t.Fatalf("extension=%q, err=%v", extension, err)
			}
		})
	}
	var tooWide bytes.Buffer
	if err := png.Encode(&tooWide, image.NewRGBA(image.Rect(0, 0, 12001, 1))); err != nil {
		t.Fatal(err)
	}
	if _, err := Check(tooWide.Bytes()); err == nil {
		t.Fatal("must reject excessive dimensions")
	}
}

func TestCheckReferences(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.MkdirAll(Directory, 0o755); err != nil {
		t.Fatal(err)
	}
	name := "photo-0123456789abcdef01234567.png"
	if err := os.WriteFile(filepath.Join(Directory, name), []byte("fixture"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		urls  []string
		valid bool
	}{
		{nil, true},
		{[]string{}, true},
		{[]string{URLPrefix + name}, true},
		{[]string{URLPrefix + "photo-000000000000000000000000.png"}, false},
		{[]string{"https://example.com/" + name}, false},
		{[]string{"javascript:alert(1)"}, false},
		{[]string{URLPrefix + "../" + name}, false},
		{[]string{URLPrefix + `..\` + name}, false},
		{[]string{URLPrefix + name + "?foo=1"}, false},
	} {
		if err := CheckReferences(tc.urls); (err == nil) != tc.valid {
			t.Fatalf("urls=%v, err=%v", tc.urls, err)
		}
	}
}
