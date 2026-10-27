package mobile

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/config"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
	"api-server/util/verificationmaterial"
)

const verificationPending uint = 4

var (
	idCardPattern   = regexp.MustCompile(`^[1-9]\d{5}(18|19|20)\d{2}(0[1-9]|1[0-2])(0[1-9]|[12]\d|3[01])\d{3}[\dXx]$`)
	bankCardPattern = regexp.MustCompile(`^\d{12,24}$`)
	tradePINPattern = regexp.MustCompile(`^\d{6}$`)
)

type verificationStatusView struct {
	Verified       uint   `json:"verified"`
	Name           string `json:"name"`
	IDCardMasked   string `json:"id_card_masked"`
	BankName       string `json:"bank_name"`
	BankCardMasked string `json:"bank_card_masked"`
	HasFront       bool   `json:"has_front"`
	HasBack        bool   `json:"has_back"`
	HasVideo       bool   `json:"has_video"`
	HasFace        bool   `json:"has_face"`
	CaptureMode    string `json:"capture_mode"`
	HasTradePIN    bool   `json:"has_trade_pin"`
	Remark         string `json:"remark"`
	FaceConfigured bool   `json:"face_configured"`
}

func VerificationStatus(c *gin.Context) {
	item, ok := currentVerificationCustomer(c)
	if !ok {
		return
	}
	response.ReturnData(c, verificationStatusView{
		Verified: item.Verified, Name: item.Name, IDCardMasked: mask(item.IDCard, 6, 4),
		BankName: item.BankName, BankCardMasked: mask(item.BankCard, 4, 4), HasFront: item.IDCardFront != "",
		HasBack: item.IDCardBack != "", HasVideo: item.VerificationVideo != "", HasTradePIN: item.TradePassword != "", Remark: item.VerificationRemark,
		HasFace: item.VerificationFace != "", CaptureMode: "local_photo", FaceConfigured: false,
	})
}

func VerificationMaterial(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	item, ok := currentVerificationCustomer(c)
	if !ok {
		return
	}
	var path string
	switch c.Param("kind") {
	case "front":
		path = item.IDCardFront
	case "back":
		path = item.IDCardBack
	case "video":
		path = item.VerificationVideo
	case "face":
		path = item.VerificationFace
	default:
		response.ReturnError(c, response.INVALID_ARGUMENT, "材料类型无效")
		return
	}
	if !isVerificationMaterialPath(path) {
		response.ReturnError(c, response.NOT_FOUND, "认证材料不存在")
		return
	}
	c.File(path)
}

func UploadVerificationMaterial(c *gin.Context) {
	kind := strings.TrimSpace(c.PostForm("kind"))
	if kind != "front" && kind != "back" && kind != "video" && kind != "face" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "材料类型无效")
		return
	}
	if kind == "face" && c.PostForm("consent") != "true" {
		response.ReturnError(c, response.FAILED_PRECONDITION, "请先同意采集正脸照片用于后台人工审核")
		return
	}
	file, err := c.FormFile("file")
	maxSize := int64(8 * 1024 * 1024)
	if kind == "video" {
		maxSize = 50 * 1024 * 1024
	} else if kind == "face" {
		maxSize = 2 * 1024 * 1024
	}
	if err != nil || file.Size <= 0 || file.Size > maxSize {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请选择符合大小限制的认证材料")
		return
	}
	source, err := file.Open()
	if err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "读取照片失败")
		return
	}
	defer source.Close()
	header := make([]byte, 512)
	read, _ := source.Read(header)
	contentType := http.DetectContentType(header[:read])
	extensions := map[string]string{"image/jpeg": ".jpg", "image/png": ".png"}
	if kind == "video" {
		extensions = map[string]string{"video/mp4": ".mp4", "video/webm": ".webm", "video/quicktime": ".mov"}
	}
	extension := extensions[contentType]
	if extension == "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "认证照片仅支持 JPEG 或 PNG，认证视频仅支持 MP4、WebM 或 MOV")
		return
	}
	if config.VerificationStorageDir == "" {
		response.ReturnError(c, response.UNAVAILABLE, "认证材料存储未配置")
		return
	}
	customerID := middleware.GetCurrentCustomerID(c)
	directory := filepath.Join(config.VerificationStorageDir, strconv.FormatUint(uint64(customerID), 10))
	if err := os.MkdirAll(directory, 0o700); err != nil {
		response.ReturnError(c, response.DATA_LOSS, "创建材料目录失败")
		return
	}
	name, err := randomToken(16)
	if err != nil {
		response.ReturnError(c, response.INTERNAL, "生成材料编号失败")
		return
	}
	path := filepath.Join(directory, name+extension)
	if kind == "face" {
		path = filepath.Join(directory, name+".jpg")
		if _, err := source.Seek(0, io.SeekStart); err != nil {
			response.ReturnError(c, response.INVALID_ARGUMENT, "读取正脸照片失败")
			return
		}
		data, err := io.ReadAll(io.LimitReader(source, maxSize+1))
		if err != nil || int64(len(data)) > maxSize {
			response.ReturnError(c, response.INVALID_ARGUMENT, "正脸照片读取失败或超过2MB")
			return
		}
		photo, err := normalizeVerificationFace(data)
		if err != nil {
			response.ReturnError(c, response.INVALID_ARGUMENT, err.Error())
			return
		}
		err = os.WriteFile(path, photo, 0o600)
		if err != nil {
			response.ReturnError(c, response.DATA_LOSS, "保存正脸照片失败")
			return
		}
	} else if err := c.SaveUploadedFile(file, path); err != nil {
		response.ReturnError(c, response.DATA_LOSS, "保存照片失败")
		return
	}
	originalPath := path
	if kind == "video" {
		transcodedPath, err := transcodeVerificationVideo(path)
		if err != nil {
			_ = os.Remove(path)
			response.ReturnError(c, response.DATA_LOSS, "认证视频转换失败，请重新选择视频后再试")
			return
		}
		path = transcodedPath
	}
	field := "id_card_front"
	if kind == "back" {
		field = "id_card_back"
	} else if kind == "video" {
		field = "verification_video"
	} else if kind == "face" {
		field = "verification_face"
	}
	var previous string
	var customer system.Customer
	if err := pgdb.GetClient().First(&customer, customerID).Error; err != nil {
		_ = os.Remove(path)
		if originalPath != path {
			_ = os.Remove(originalPath)
		}
		response.ReturnError(c, response.NOT_FOUND, "用户不存在")
		return
	}
	if kind == "front" {
		previous = customer.IDCardFront
	} else if kind == "back" {
		previous = customer.IDCardBack
	} else if kind == "face" {
		previous = customer.VerificationFace
	} else {
		previous = customer.VerificationVideo
	}
	updates := map[string]any{field: path, "verified": system.StatusDisabled, "verification_remark": "", "verification_certify_id": ""}
	if kind == "face" {
		updates["verification_face_at"] = time.Now().Unix()
	}
	if err := pgdb.GetClient().Model(&customer).Updates(updates).Error; err != nil {
		_ = os.Remove(path)
		if originalPath != path {
			_ = os.Remove(originalPath)
		}
		response.ReturnError(c, response.DATA_LOSS, "保存材料记录失败")
		return
	}
	if originalPath != path {
		_ = os.Remove(originalPath)
	}
	if previous != "" && previous != path && strings.HasPrefix(previous, config.VerificationStorageDir) {
		_ = os.Remove(previous)
	}
	response.ReturnData(c, gin.H{"uploaded": true, "kind": kind})
}

func transcodeVerificationVideo(inputPath string) (string, error) {
	outputPath := strings.TrimSuffix(inputPath, filepath.Ext(inputPath)) + ".h264.mp4"
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	command := exec.CommandContext(ctx, "ffmpeg",
		"-y",
		"-i", inputPath,
		"-map", "0:v:0",
		"-map", "0:a?",
		"-c:v", "libx264",
		"-pix_fmt", "yuv420p",
		"-preset", "veryfast",
		"-crf", "23",
		"-c:a", "aac",
		"-movflags", "+faststart",
		outputPath,
	)
	if err := command.Run(); err != nil {
		_ = os.Remove(outputPath)
		if ctx.Err() != nil {
			return "", fmt.Errorf("认证视频转换超时: %w", ctx.Err())
		}
		return "", fmt.Errorf("认证视频转换失败: %w", err)
	}
	return outputPath, nil
}

func SaveVerificationProfile(c *gin.Context) {
	var input struct {
		Name            string `json:"name"`
		IDCard          string `json:"id_card"`
		BankName        string `json:"bank_name"`
		BankCard        string `json:"bank_card"`
		TradePIN        string `json:"trade_pin"`
		ConfirmTradePIN string `json:"confirm_trade_pin"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "开户资料格式无效")
		return
	}
	input.Name, input.IDCard = strings.TrimSpace(input.Name), strings.TrimSpace(input.IDCard)
	input.BankName, input.BankCard = strings.TrimSpace(input.BankName), strings.TrimSpace(input.BankCard)
	if input.Name == "" || !idCardPattern.MatchString(input.IDCard) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请输入正确的姓名和身份证号")
		return
	}
	if input.BankName == "" || !bankCardPattern.MatchString(input.BankCard) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请输入正确的银行和借记卡号")
		return
	}
	if !tradePINPattern.MatchString(input.TradePIN) || input.TradePIN != input.ConfirmTradePIN {
		response.ReturnError(c, response.INVALID_ARGUMENT, "交易密码必须为两次一致的 6 位数字")
		return
	}
	hash, err := system.HashPassword(input.TradePIN)
	if err != nil {
		response.ReturnError(c, response.INTERNAL, "交易密码加密失败")
		return
	}
	updates := map[string]any{"name": input.Name, "id_card": strings.ToUpper(input.IDCard), "bank_name": input.BankName, "bank_card": input.BankCard, "trade_password": hash, "verified": system.StatusDisabled, "verification_certify_id": "", "verification_remark": ""}
	if err := pgdb.GetClient().Model(&system.Customer{}).Where("id = ?", middleware.GetCurrentCustomerID(c)).Updates(updates).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "保存开户资料失败")
		return
	}
	response.ReturnData(c, nil)
}

func SaveVerificationIdentity(c *gin.Context) {
	var input struct {
		Name   string `json:"name"`
		IDCard string `json:"id_card"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "身份资料格式无效")
		return
	}
	input.Name, input.IDCard = strings.TrimSpace(input.Name), strings.ToUpper(strings.TrimSpace(input.IDCard))
	var customer system.Customer
	if err := pgdb.GetClient().First(&customer, middleware.GetCurrentCustomerID(c)).Error; err != nil {
		response.ReturnError(c, response.NOT_FOUND, "用户不存在")
		return
	}
	if input.Name == "" {
		input.Name = customer.Name
	}
	if input.IDCard == "" {
		input.IDCard = customer.IDCard
	}
	if input.Name == "" || !idCardPattern.MatchString(input.IDCard) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请输入正确的姓名和身份证号")
		return
	}
	if err := validateVerificationMaterials(customer); err != nil {
		response.ReturnError(c, response.FAILED_PRECONDITION, err.Error())
		return
	}
	if err := verificationmaterial.CheckFace(config.VerificationStorageDir, customer.VerificationFace); err != nil {
		response.ReturnError(c, response.FAILED_PRECONDITION, err.Error())
		return
	}
	if err := pgdb.GetClient().Model(&customer).Updates(map[string]any{"name": input.Name, "id_card": input.IDCard, "verified": verificationPending, "verification_remark": "等待后台审核"}).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "保存身份资料失败")
		return
	}
	response.ReturnData(c, nil)
}

func validateVerificationMaterials(customer system.Customer) error {
	if customer.IDCardFront == "" || customer.IDCardBack == "" {
		return fmt.Errorf("请先上传身份证正反面")
	}
	if strings.TrimSpace(customer.VerificationFace) == "" {
		return fmt.Errorf("请先拍摄并上传正脸照片")
	}
	return nil
}

func UpdateVerificationBankCard(c *gin.Context) {
	var input struct {
		BankName   string `json:"bank_name"`
		BankCard   string `json:"bank_card"`
		CurrentPIN string `json:"current_trade_pin"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "银行卡资料格式无效")
		return
	}
	input.BankName, input.BankCard = strings.TrimSpace(input.BankName), strings.TrimSpace(input.BankCard)
	item, ok := currentVerificationCustomer(c)
	if !ok {
		return
	}
	if item.TradePassword == "" || !system.VerifyPassword(input.CurrentPIN, item.TradePassword, "bcrypt") {
		response.ReturnError(c, response.PERMISSION_DENIED, "当前交易密码错误")
		return
	}
	if input.BankName == "" || !bankCardPattern.MatchString(input.BankCard) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请输入正确的银行和借记卡号")
		return
	}
	if err := pgdb.GetClient().Model(&item).Updates(map[string]any{"bank_name": input.BankName, "bank_card": input.BankCard}).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "修改银行卡失败")
		return
	}
	response.ReturnData(c, nil)
}

func UpdateVerificationTradePassword(c *gin.Context) {
	var input struct {
		CurrentPIN string `json:"current_trade_pin"`
		NewPIN     string `json:"new_trade_pin"`
		ConfirmPIN string `json:"confirm_trade_pin"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "交易密码格式无效")
		return
	}
	item, ok := currentVerificationCustomer(c)
	if !ok {
		return
	}
	if item.TradePassword == "" || !system.VerifyPassword(input.CurrentPIN, item.TradePassword, "bcrypt") {
		response.ReturnError(c, response.PERMISSION_DENIED, "当前交易密码错误")
		return
	}
	if !tradePINPattern.MatchString(input.NewPIN) || input.NewPIN != input.ConfirmPIN {
		response.ReturnError(c, response.INVALID_ARGUMENT, "新交易密码必须为两次一致的 6 位数字")
		return
	}
	if input.NewPIN == input.CurrentPIN {
		response.ReturnError(c, response.INVALID_ARGUMENT, "新交易密码不能与当前密码相同")
		return
	}
	hash, err := system.HashPassword(input.NewPIN)
	if err != nil {
		response.ReturnError(c, response.INTERNAL, "交易密码加密失败")
		return
	}
	if err := pgdb.GetClient().Model(&item).Update("trade_password", hash).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "修改交易密码失败")
		return
	}
	response.ReturnData(c, nil)
}

func SetupVerificationTradePassword(c *gin.Context) {
	var input struct {
		NewPIN     string `json:"new_trade_pin"`
		ConfirmPIN string `json:"confirm_trade_pin"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "交易密码格式无效")
		return
	}
	item, ok := currentVerificationCustomer(c)
	if !ok {
		return
	}
	if item.Verified != system.StatusEnabled {
		response.ReturnError(c, response.FAILED_PRECONDITION, "请先完成实名认证")
		return
	}
	if item.TradePassword != "" {
		response.ReturnError(c, response.FAILED_PRECONDITION, "资金密码已设置，请通过修改资金密码更新")
		return
	}
	if !tradePINPattern.MatchString(input.NewPIN) || input.NewPIN != input.ConfirmPIN {
		response.ReturnError(c, response.INVALID_ARGUMENT, "资金密码必须为两次一致的 6 位数字")
		return
	}
	hash, err := system.HashPassword(input.NewPIN)
	if err != nil {
		response.ReturnError(c, response.INTERNAL, "资金密码加密失败")
		return
	}
	if err := pgdb.GetClient().Model(&item).Update("trade_password", hash).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "设置资金密码失败")
		return
	}
	response.ReturnData(c, nil)
}

func StartFaceVerification(c *gin.Context) {
	response.ReturnError(c, response.FAILED_PRECONDITION, "当前使用正脸照片采集和人工审核，请更新客户端后提交材料")
}

func ConfirmFaceVerification(c *gin.Context) {
	response.ReturnError(c, response.FAILED_PRECONDITION, "正脸采集不等于实名核验，认证结果请以后台人工审核为准")
}

func currentVerificationCustomer(c *gin.Context) (system.Customer, bool) {
	var item system.Customer
	if err := pgdb.GetClient().First(&item, middleware.GetCurrentCustomerID(c)).Error; err != nil {
		response.ReturnError(c, response.NOT_FOUND, "用户不存在")
		return item, false
	}
	return item, true
}

func isVerificationMaterialPath(path string) bool {
	if path == "" || config.VerificationStorageDir == "" {
		return false
	}
	relative, err := filepath.Rel(filepath.Clean(config.VerificationStorageDir), filepath.Clean(path))
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func faceRecognitionConfigured() bool {
	return config.FaceRecognitionEnabled && config.FaceRecognitionAPIKey != "" && config.FaceRecognitionSecretKey != ""
}

func baiduAccessToken() (string, error) {
	endpoint := "https://aip.baidubce.com/oauth/2.0/token"
	form := url.Values{"grant_type": {"client_credentials"}, "client_id": {config.FaceRecognitionAPIKey}, "client_secret": {config.FaceRecognitionSecretKey}}
	result, err := http.PostForm(endpoint, form)
	if err != nil {
		return "", err
	}
	defer result.Body.Close()
	var token struct {
		AccessToken string `json:"access_token"`
	}
	if err := json.NewDecoder(result.Body).Decode(&token); err != nil || token.AccessToken == "" {
		return "", fmt.Errorf("invalid access token response")
	}
	return token.AccessToken, nil
}

func extractIDCard(path string) (string, string, error) {
	image, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	token, err := baiduAccessToken()
	if err != nil {
		return "", "", err
	}
	body, err := json.Marshal(map[string]string{
		"image": base64.StdEncoding.EncodeToString(image), "id_card_side": "front",
	})
	if err != nil {
		return "", "", err
	}
	requestURL := strings.TrimRight(config.FaceRecognitionEndpoint, "/") + "/rest/2.0/ocr/v1/idcard?access_token=" + url.QueryEscape(token)
	request, err := http.NewRequest(http.MethodPost, requestURL, strings.NewReader(string(body)))
	if err != nil {
		return "", "", err
	}
	request.Header.Set("Content-Type", "application/json")
	result, err := http.DefaultClient.Do(request)
	if err != nil {
		return "", "", err
	}
	defer result.Body.Close()
	var ocr struct {
		ErrorCode   int `json:"error_code"`
		WordsResult map[string]struct {
			Words string `json:"words"`
		} `json:"words_result"`
	}
	if err := json.NewDecoder(result.Body).Decode(&ocr); err != nil || ocr.ErrorCode != 0 {
		return "", "", fmt.Errorf("百度身份证识别失败")
	}
	name := strings.TrimSpace(ocr.WordsResult["姓名"].Words)
	idCard := strings.ToUpper(strings.TrimSpace(ocr.WordsResult["公民身份号码"].Words))
	if name == "" || !idCardPattern.MatchString(idCard) {
		return "", "", fmt.Errorf("未能提取有效身份证信息")
	}
	return name, idCard, nil
}

func randomToken(size int) (string, error) {
	buffer := make([]byte, size)
	if _, err := rand.Read(buffer); err != nil {
		return "", err
	}
	return hex.EncodeToString(buffer), nil
}

func mask(value string, start, end int) string {
	if len(value) <= start+end {
		return value
	}
	return value[:start] + strings.Repeat("*", len(value)-start-end) + value[len(value)-end:]
}
