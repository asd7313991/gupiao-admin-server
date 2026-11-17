package setting

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"api-server/api/response"
	"api-server/db/pgdb/system"
	"github.com/gin-gonic/gin"
)

func TestSoundUploadValidation(t *testing.T) {
	router := gin.New()
	router.POST("/sounds/:kind", UploadSound)
	for _, data := range [][]byte{nil, []byte("<html>not audio</html>"), make([]byte, maxSoundBytes+1)} {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("file", "fake.mp3")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := part.Write(data); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, "/sounds/recharge", &body)
		request.Header.Set("Content-Type", writer.FormDataContentType())
		recorder := httptest.NewRecorder()
		router.ServeHTTP(recorder, request)
		var result struct{ Code int }
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil || result.Code == 200 {
			t.Fatalf("无效音频应被拒绝：%s，%v", recorder.Body.String(), err)
		}
	}
}

func TestSoundBinaryResponse(t *testing.T) {
	data := []byte{'R', 'I', 'F', 'F', 0, 0xff, 0x80, 'W', 'A', 'V', 'E'}
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	response.ReturnData(context, soundPayload(system.AdminSound{Data: data, ContentType: "audio/wave", AudioHash: "hash"}))
	var result struct {
		Data struct {
			Data string `json:"data"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	decoded, err := base64.StdEncoding.DecodeString(result.Data.Data)
	if err != nil || !bytes.Equal(decoded, data) {
		t.Fatalf("音频经过响应序列化后损坏：%v", err)
	}
}
