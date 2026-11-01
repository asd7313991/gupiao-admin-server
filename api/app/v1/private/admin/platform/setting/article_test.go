package setting

import (
	"bytes"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"

	"api-server/api/app/v1/open/mobile"
	"api-server/config"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"api-server/util/articleimage"
)

func uploadTestImage(t *testing.T, router http.Handler, data []byte) string {
	t.Helper()
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", "../../untrusted.html")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(data); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest(http.MethodPost, "/upload", &body)
	request.Header.Set("Content-Type", writer.FormDataContentType())
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, request)
	var result struct {
		Code int `json:"code"`
		Data struct {
			URL string `json:"url"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Code != 200 {
		t.Fatalf("upload failed: %s", recorder.Body.String())
	}
	if !articleimage.ValidName(strings.TrimPrefix(result.Data.URL, articleimage.URLPrefix)) {
		t.Fatalf("unexpected uploaded filename: %s", result.Data.URL)
	}
	return result.Data.URL
}

func TestUploadArticleImage(t *testing.T) {
	t.Chdir(t.TempDir())
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/upload", UploadArticleImage)
	router.GET("/api/v1/open/mobile/article-images/:name", mobile.MobileArticleImage)
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 3, 2))); err != nil {
		t.Fatal(err)
	}
	url := uploadTestImage(t, router, picture.Bytes())
	second := uploadTestImage(t, router, picture.Bytes())
	if url == second {
		t.Fatal("uploads must have unique names")
	}
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, url, nil))
	if recorder.Code != 200 || !bytes.Equal(recorder.Body.Bytes(), picture.Bytes()) ||
		recorder.Header().Get("X-Content-Type-Options") != "nosniff" ||
		recorder.Header().Get("Content-Type") != "image/png" {
		t.Fatal("public image response is incorrect")
	}
	for _, data := range [][]byte{nil, []byte("<svg/>"), make([]byte, articleimage.MaxBytes+1)} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "fake.png")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/upload", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		var result struct{ Code int }
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil || result.Code != 400 {
			t.Fatalf("invalid upload must fail: %s", recorder.Body.String())
		}
	}
}

func TestArticlePhotosIntegration(t *testing.T) {
	dsn := os.Getenv("ARTICLE_TEST_DSN")
	if dsn == "" {
		t.Skip("未配置文章隔离测试数据库")
	}
	t.Chdir(t.TempDir())
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	rootSQL, err := root.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer rootSQL.Close()
	schema := fmt.Sprintf("article_photos_%d", time.Now().UnixNano())
	if err := root.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	}()
	config.PgsqlDSN = dsn + " search_path=" + schema
	db := pgdb.GetClient()
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := db.Exec(`CREATE TABLE app_articles (
		id bigserial PRIMARY KEY, created_at timestamptz, updated_at timestamptz,
		deleted_at timestamptz, title text, type text, content text, status bigint DEFAULT 1
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("INSERT INTO app_articles (title,type,content,status) VALUES ('旧文章','法律声明','<p>旧正文</p>',1)").Error; err != nil {
		t.Fatal(err)
	}
	if err := db.AutoMigrate(&system.AppArticle{}); err != nil {
		t.Fatal(err)
	}
	var legacy system.AppArticle
	if err := db.First(&legacy).Error; err != nil || len(legacy.FooterImages) != 0 || legacy.Content != "<p>旧正文</p>" {
		t.Fatal("migration must preserve old content and initialize empty images")
	}
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.POST("/upload", UploadArticleImage)
	router.POST("/save", SaveArticle)
	router.GET("/list", ListArticles)
	router.GET("/article", mobile.MobileArticle)
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 2, 2))); err != nil {
		t.Fatal(err)
	}
	first, second := uploadTestImage(t, router, picture.Bytes()), uploadTestImage(t, router, picture.Bytes())
	call := func(method, path string, input any) []byte {
		t.Helper()
		body, err := json.Marshal(input)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(method, path, bytes.NewReader(body))
		request.Header.Set("Content-Type", "application/json")
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		return recorder.Body.Bytes()
	}
	input := map[string]any{"title": "照片测试", "type": "法律声明", "content": "<p>测试正文</p>", "status": 1, "footer_images": []string{first, second}}
	var saved struct {
		Code int
		Data struct {
			ID           uint     `json:"id"`
			FooterImages []string `json:"footer_images"`
		}
	}
	if err := json.Unmarshal(call("POST", "/save", input), &saved); err != nil || saved.Code != 200 || !reflect.DeepEqual(saved.Data.FooterImages, []string{first, second}) {
		t.Fatal("create response must contain ordered image array")
	}
	input["id"] = saved.Data.ID
	assertImages := func(expected []string) {
		t.Helper()
		var record system.AppArticle
		if err := db.First(&record, saved.Data.ID).Error; err != nil || !reflect.DeepEqual(record.FooterImages, expected) {
			t.Fatalf("persisted images mismatch: %v, %v", record.FooterImages, err)
		}
		var public struct {
			Code int
			Data struct {
				FooterImages []string `json:"footer_images"`
			}
		}
		if err := json.Unmarshal(call("GET", "/article?type=%E6%B3%95%E5%BE%8B%E5%A3%B0%E6%98%8E", nil), &public); err != nil || public.Code != 200 || !reflect.DeepEqual(public.Data.FooterImages, expected) {
			t.Fatal("mobile response must retain image order")
		}
	}
	assertImages([]string{first, second})
	input["footer_images"] = []string{second, first}
	call("POST", "/save", input)
	assertImages([]string{second, first})
	delete(input, "footer_images")
	input["content"] = "<p>只改正文</p>"
	call("POST", "/save", input)
	assertImages([]string{second, first})
	for _, invalid := range []string{"https://example.com/foreign.png", articleimage.URLPrefix + "photo-000000000000000000000000.png"} {
		input["footer_images"] = []string{invalid}
		var result struct{ Code int }
		if err := json.Unmarshal(call("POST", "/save", input), &result); err != nil || result.Code != 400 {
			t.Fatal("invalid image references must be rejected")
		}
		assertImages([]string{second, first})
	}
	input["footer_images"] = []string{second}
	call("POST", "/save", input)
	assertImages([]string{second})
	input["footer_images"] = []string{}
	call("POST", "/save", input)
	assertImages([]string{})
}
