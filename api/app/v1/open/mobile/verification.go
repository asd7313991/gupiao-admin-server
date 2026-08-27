package mobile

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/config"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
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
		HasBack: item.IDCardBack != "", HasTradePIN: item.TradePassword != "", Remark: item.VerificationRemark,
		FaceConfigured: faceRecognitionConfigured(),
	})
}

func UploadVerificationMaterial(c *gin.Context) {
	kind := strings.TrimSpace(c.PostForm("kind"))
	if kind != "front" && kind != "back" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "材料类型无效")
		return
	}
	file, err := c.FormFile("file")
	if err != nil || file.Size <= 0 || file.Size > 8*1024*1024 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请选择不超过 8MB 的身份证照片")
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
	extension := map[string]string{"image/jpeg": ".jpg", "image/png": ".png"}[contentType]
	if extension == "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "仅支持 JPEG 或 PNG 身份证照片")
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
	if err := c.SaveUploadedFile(file, path); err != nil {
		response.ReturnError(c, response.DATA_LOSS, "保存照片失败")
		return
	}
	field := "id_card_front"
	if kind == "back" {
		field = "id_card_back"
	}
	var previous string
	var customer system.Customer
	if err := pgdb.GetClient().First(&customer, customerID).Error; err != nil {
		_ = os.Remove(path)
		response.ReturnError(c, response.NOT_FOUND, "用户不存在")
		return
	}
	if kind == "front" {
		previous = customer.IDCardFront
	} else {
		previous = customer.IDCardBack
	}
	if err := pgdb.GetClient().Model(&customer).Updates(map[string]any{field: path, "verified": system.StatusDisabled, "verification_remark": ""}).Error; err != nil {
		_ = os.Remove(path)
		response.ReturnError(c, response.DATA_LOSS, "保存材料记录失败")
		return
	}
	if previous != "" && previous != path && strings.HasPrefix(previous, config.VerificationStorageDir) {
		_ = os.Remove(previous)
	}
	response.ReturnData(c, gin.H{"uploaded": true, "kind": kind})
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

func StartFaceVerification(c *gin.Context) {
	if !faceRecognitionConfigured() {
		response.ReturnError(c, response.UNAVAILABLE, "百度云人脸实名认证服务未配置")
		return
	}
	item, ok := currentVerificationCustomer(c)
	if !ok {
		return
	}
	if item.Name == "" || item.IDCard == "" || item.BankCard == "" || item.TradePassword == "" || item.IDCardFront == "" || item.IDCardBack == "" {
		response.ReturnError(c, response.FAILED_PRECONDITION, "请先完成身份、证件、银行卡和交易密码资料")
		return
	}
	file, err := c.FormFile("file")
	if err != nil || file.Size <= 0 || file.Size > 2*1024*1024 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请选择不超过 2MB 的本人正面照片")
		return
	}
	source, err := file.Open()
	if err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "读取人脸照片失败")
		return
	}
	defer source.Close()
	image, err := io.ReadAll(io.LimitReader(source, 2*1024*1024+1))
	if err != nil || len(image) == 0 || len(image) > 2*1024*1024 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "读取人脸照片失败")
		return
	}
	contentType := http.DetectContentType(image)
	if contentType != "image/jpeg" && contentType != "image/png" && contentType != "image/bmp" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "仅支持 JPG、PNG 或 BMP 人脸照片")
		return
	}
	token, err := baiduAccessToken()
	if err != nil {
		response.ReturnError(c, response.UNAVAILABLE, "获取百度云访问令牌失败")
		return
	}
	body, _ := json.Marshal(map[string]any{
		"image": base64.StdEncoding.EncodeToString(image), "image_type": "BASE64",
		"id_card_number": item.IDCard, "name": item.Name,
		"quality_control": "HIGH", "liveness_control": "HIGH", "spoofing_control": "NORMAL",
	})
	requestURL := strings.TrimRight(config.FaceRecognitionEndpoint, "/") + "/rest/2.0/face/v3/person/verify?access_token=" + url.QueryEscape(token)
	request, _ := http.NewRequest(http.MethodPost, requestURL, strings.NewReader(string(body)))
	request.Header.Set("Content-Type", "application/json")
	result, err := http.DefaultClient.Do(request)
	if err != nil || result == nil {
		response.ReturnError(c, response.UNAVAILABLE, "调用百度云人脸实名认证失败")
		return
	}
	defer result.Body.Close()
	var verification struct {
		ErrorCode int    `json:"error_code"`
		ErrorMsg  string `json:"error_msg"`
		Result    struct {
			Score float64 `json:"score"`
		} `json:"result"`
	}
	if err := json.NewDecoder(result.Body).Decode(&verification); err != nil || verification.ErrorCode != 0 {
		response.ReturnError(c, response.UNAVAILABLE, "百度云实名认证失败: "+verification.ErrorMsg)
		return
	}
	passed := verification.Result.Score >= config.FaceRecognitionScoreThreshold
	status, remark := verificationPending, fmt.Sprintf("人脸相似度 %.2f，未达到认证阈值", verification.Result.Score)
	if passed {
		status, remark = system.StatusEnabled, ""
	}
	if err := pgdb.GetClient().Model(&item).Updates(map[string]any{"verification_certify_id": fmt.Sprintf("baidu:%.2f", verification.Result.Score), "verified": status, "verification_remark": remark}).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "保存认证结果失败")
		return
	}
	response.ReturnData(c, gin.H{"passed": passed, "verified": status, "score": verification.Result.Score})
}

func ConfirmFaceVerification(c *gin.Context) {
	item, ok := currentVerificationCustomer(c)
	if !ok {
		return
	}
	if item.VerificationCertifyID == "" {
		response.ReturnError(c, response.FAILED_PRECONDITION, "尚未发起人脸核身")
		return
	}
	if !strings.HasPrefix(item.VerificationCertifyID, "baidu:") {
		response.ReturnError(c, response.FAILED_PRECONDITION, "认证任务不是百度云认证任务")
		return
	}
	response.ReturnData(c, gin.H{"passed": item.Verified == system.StatusEnabled, "verified": item.Verified})
}

func currentVerificationCustomer(c *gin.Context) (system.Customer, bool) {
	var item system.Customer
	if err := pgdb.GetClient().First(&item, middleware.GetCurrentCustomerID(c)).Error; err != nil {
		response.ReturnError(c, response.NOT_FOUND, "用户不存在")
		return item, false
	}
	return item, true
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
