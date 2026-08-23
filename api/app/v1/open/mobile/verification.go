package mobile

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	cloudauth "github.com/alibabacloud-go/cloudauth-20190307/v3/client"
	openapi "github.com/alibabacloud-go/darabonba-openapi/v2/client"
	"github.com/alibabacloud-go/tea/tea"
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
		response.ReturnError(c, response.UNAVAILABLE, "阿里云人脸核身服务未配置")
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
	client, err := newFaceClient()
	if err != nil {
		response.ReturnError(c, response.UNAVAILABLE, "初始化人脸核身服务失败")
		return
	}
	orderToken, _ := randomToken(12)
	request := &cloudauth.InitFaceVerifyRequest{
		SceneId: tea.Int64(config.FaceRecognitionSceneID), OuterOrderNo: tea.String(fmt.Sprintf("customer-%d-%s", item.ID, orderToken)),
		ProductCode: tea.String("ID_PRO"), Model: tea.String("LIVENESS"), CertType: tea.String("IDENTITY_CARD"),
		CertName: tea.String(item.Name), CertNo: tea.String(item.IDCard), ReturnUrl: tea.String(config.FaceRecognitionReturnURL),
		Mobile: tea.String(item.Phone), Ip: tea.String(c.ClientIP()),
	}
	result, err := client.InitFaceVerify(request)
	if err != nil || result == nil || result.Body == nil || result.Body.Code == nil || *result.Body.Code != "200" || result.Body.ResultObject == nil || result.Body.ResultObject.CertifyId == nil || result.Body.ResultObject.CertifyUrl == nil {
		response.ReturnError(c, response.UNAVAILABLE, "创建人脸核身任务失败")
		return
	}
	certifyID := *result.Body.ResultObject.CertifyId
	if err := pgdb.GetClient().Model(&item).Updates(map[string]any{"verification_certify_id": certifyID, "verified": verificationPending, "verification_remark": "等待人脸核身"}).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "保存核身任务失败")
		return
	}
	response.ReturnData(c, gin.H{"certify_url": *result.Body.ResultObject.CertifyUrl})
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
	client, err := newFaceClient()
	if err != nil {
		response.ReturnError(c, response.UNAVAILABLE, "初始化人脸核身服务失败")
		return
	}
	result, err := client.DescribeFaceVerify(&cloudauth.DescribeFaceVerifyRequest{SceneId: tea.Int64(config.FaceRecognitionSceneID), CertifyId: tea.String(item.VerificationCertifyID)})
	if err != nil || result == nil || result.Body == nil || result.Body.ResultObject == nil {
		response.ReturnError(c, response.UNAVAILABLE, "查询人脸核身结果失败")
		return
	}
	passed := result.Body.ResultObject.Passed != nil && *result.Body.ResultObject.Passed == "T"
	status, remark := verificationPending, "人脸核身尚未通过"
	if passed {
		status, remark = system.StatusEnabled, ""
	}
	if err := pgdb.GetClient().Model(&item).Updates(map[string]any{"verified": status, "verification_remark": remark}).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "更新认证状态失败")
		return
	}
	response.ReturnData(c, gin.H{"passed": passed, "verified": status})
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
	return config.FaceRecognitionEnabled && config.FaceRecognitionAccessKeyID != "" && config.FaceRecognitionAccessKeySecret != "" && config.FaceRecognitionSceneID > 0 && config.FaceRecognitionReturnURL != ""
}

func newFaceClient() (*cloudauth.Client, error) {
	clientConfig := &openapi.Config{AccessKeyId: tea.String(config.FaceRecognitionAccessKeyID), AccessKeySecret: tea.String(config.FaceRecognitionAccessKeySecret), Endpoint: tea.String(config.FaceRecognitionEndpoint)}
	return cloudauth.NewClient(clientConfig)
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
