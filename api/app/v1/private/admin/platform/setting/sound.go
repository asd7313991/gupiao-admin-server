package setting

import (
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const maxSoundBytes = 2 * 1024 * 1024

func ListSounds(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	var rows []system.AdminSound
	if err := pgdb.GetClient().Omit("data").Find(&rows).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取声音提醒失败")
		return
	}
	result := make([]system.AdminSound, 0, len(system.AdminSoundKinds))
	for _, kind := range system.AdminSoundKinds {
		row := system.AdminSound{Kind: kind}
		for _, stored := range rows {
			if stored.Kind == kind {
				row = stored
				break
			}
		}
		result = append(result, row)
	}
	response.ReturnData(c, result)
}

func ReadSound(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	if !system.ValidAdminSoundKind(c.Param("kind")) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "声音类型无效")
		return
	}
	var sound system.AdminSound
	err := pgdb.GetClient().First(&sound, "kind = ?", c.Param("kind")).Error
	if errors.Is(err, gorm.ErrRecordNotFound) || (err == nil && len(sound.Data) == 0) {
		response.ReturnError(c, response.NOT_FOUND, "尚未上传提示音")
		return
	}
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取提示音失败")
		return
	}
	response.ReturnData(c, soundPayload(sound))
}

func soundPayload(sound system.AdminSound) gin.H {
	return gin.H{"data": base64.StdEncoding.EncodeToString(sound.Data), "content_type": sound.ContentType, "audio_hash": sound.AudioHash}
}

func soundContentType(data []byte) (string, error) {
	if len(data) == 0 || len(data) > maxSoundBytes {
		return "", fmt.Errorf("请选择不超过 2MB 的音频")
	}
	contentType := http.DetectContentType(data)
	if contentType != "audio/mpeg" && contentType != "audio/wave" && contentType != "audio/x-wav" {
		return "", fmt.Errorf("提示音仅支持 MP3 或 WAV 格式，请勿仅修改文件后缀")
	}
	return contentType, nil
}

func UploadSound(c *gin.Context) {
	kind := c.Param("kind")
	if !system.ValidAdminSoundKind(kind) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "声音类型无效")
		return
	}
	c.Request.Body = http.MaxBytesReader(c.Writer, c.Request.Body, maxSoundBytes+64*1024)
	file, err := c.FormFile("file")
	if c.Request.MultipartForm != nil {
		defer c.Request.MultipartForm.RemoveAll()
	}
	if err != nil || file.Size <= 0 || file.Size > maxSoundBytes {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请选择不超过 2MB 的 MP3 或 WAV 音频")
		return
	}
	source, err := file.Open()
	if err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "读取音频失败")
		return
	}
	defer source.Close()
	data, err := io.ReadAll(io.LimitReader(source, maxSoundBytes+1))
	if err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "读取音频失败")
		return
	}
	contentType, err := soundContentType(data)
	if err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, err.Error())
		return
	}
	sound := system.AdminSound{
		Kind: kind, Filename: filepath.Base(strings.ReplaceAll(file.Filename, "\\", "/")),
		ContentType: contentType, AudioHash: fmt.Sprintf("%x", sha256.Sum256(data)), Data: data,
	}
	// 更换文件不会覆盖并发增长的业务计数或管理员设置的开关。
	if err := pgdb.GetClient().Clauses(clause.OnConflict{
		Columns:   []clause.Column{{Name: "kind"}},
		DoUpdates: clause.AssignmentColumns([]string{"filename", "content_type", "audio_hash", "data"}),
	}).Create(&sound).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "保存提示音失败")
		return
	}
	response.ReturnData(c, nil)
}

func SetSoundEnabled(c *gin.Context) {
	var input struct {
		Enabled *bool `json:"enabled"`
	}
	kind := c.Param("kind")
	if !system.ValidAdminSoundKind(kind) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "声音类型无效")
		return
	}
	if !middleware.CheckParam(&input, c) {
		return
	}
	if input.Enabled == nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请指定提示音开关")
		return
	}
	result := pgdb.GetClient().Model(&system.AdminSound{}).Where("kind = ? AND audio_hash <> ''", kind).Update("enabled", *input.Enabled)
	if result.Error != nil {
		response.ReturnError(c, response.DATA_LOSS, "保存提示音开关失败")
		return
	}
	if result.RowsAffected == 0 {
		response.ReturnError(c, response.FAILED_PRECONDITION, "请先上传提示音")
		return
	}
	response.ReturnData(c, nil)
}
