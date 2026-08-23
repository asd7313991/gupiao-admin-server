package mobile

import (
	"regexp"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"api-server/api/auth"
	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

var phonePattern = regexp.MustCompile(`^1\d{10}$`)

type authInput struct {
	Phone    string `json:"phone"`
	Password string `json:"password"`
	Name     string `json:"name"`
}

type customerView struct {
	ID          uint    `json:"id"`
	Phone       string  `json:"phone"`
	Name        string  `json:"name"`
	Balance     float64 `json:"balance"`
	TotalProfit float64 `json:"total_profit"`
	TotalLoss   float64 `json:"total_loss"`
	Verified    uint    `json:"verified"`
	Status      uint    `json:"status"`
}

func customerData(item system.Customer) customerView {
	return customerView{ID: item.ID, Phone: item.Phone, Name: item.Name, Balance: item.Balance, TotalProfit: item.TotalProfit, TotalLoss: item.TotalLoss, Verified: item.Verified, Status: item.Status}
}

func Register(c *gin.Context) {
	var input authInput
	if !middleware.CheckParam(&input, c) {
		return
	}
	input.Phone = strings.TrimSpace(input.Phone)
	if !phonePattern.MatchString(input.Phone) || len(input.Password) < 6 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请输入正确手机号，密码至少为 6 位")
		return
	}
	var count int64
	if err := pgdb.GetClient().Model(&system.Customer{}).Where("phone = ?", input.Phone).Count(&count).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "注册失败，请稍后重试")
		return
	}
	if count > 0 {
		response.ReturnError(c, response.ALREADY_EXISTS, "该手机号已经注册")
		return
	}
	hash, err := system.HashPassword(input.Password)
	if err != nil {
		response.ReturnError(c, response.INTERNAL, "注册失败，请稍后重试")
		return
	}
	name := strings.TrimSpace(input.Name)
	if name == "" {
		name = "用户" + input.Phone[7:]
	}
	item := system.Customer{Phone: input.Phone, Name: name, Password: hash, GroupName: "普通用户", Status: system.StatusEnabled, FundStatus: system.StatusEnabled, Verified: system.StatusDisabled}
	if err := pgdb.GetClient().Create(&item).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "注册失败，请稍后重试")
		return
	}
	returnAuth(c, item)
}

func Login(c *gin.Context) {
	var input authInput
	if !middleware.CheckParam(&input, c) {
		return
	}
	var item system.Customer
	err := pgdb.GetClient().Where("phone = ?", strings.TrimSpace(input.Phone)).First(&item).Error
	if err != nil || !system.VerifyPassword(input.Password, item.Password, "bcrypt") {
		response.ReturnError(c, response.UNAUTHENTICATED, "手机号或密码错误")
		return
	}
	if item.Status != system.StatusEnabled {
		response.ReturnError(c, response.PERMISSION_DENIED, "账号已被停用")
		return
	}
	returnAuth(c, item)
}

func returnAuth(c *gin.Context, item system.Customer) {
	token, err := auth.CustomerJWTIssue(item.ID, item.Phone)
	if err != nil {
		response.ReturnError(c, response.INTERNAL, "登录失败，请稍后重试")
		return
	}
	response.ReturnData(c, gin.H{"access_token": token, "user": customerData(item)})
}

func Profile(c *gin.Context) {
	var item system.Customer
	err := pgdb.GetClient().First(&item, middleware.GetCurrentCustomerID(c)).Error
	if err == gorm.ErrRecordNotFound {
		response.ReturnError(c, response.NOT_FOUND, "用户不存在")
		return
	}
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取用户信息失败")
		return
	}
	response.ReturnData(c, customerData(item))
}

func UpdateLoginPassword(c *gin.Context) {
	var input struct {
		CurrentPassword string `json:"current_password"`
		NewPassword     string `json:"new_password"`
		ConfirmPassword string `json:"confirm_password"`
	}
	if err := c.ShouldBindJSON(&input); err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, "密码格式无效")
		return
	}
	var item system.Customer
	if err := pgdb.GetClient().First(&item, middleware.GetCurrentCustomerID(c)).Error; err != nil {
		response.ReturnError(c, response.NOT_FOUND, "用户不存在")
		return
	}
	if !system.VerifyPassword(input.CurrentPassword, item.Password, "bcrypt") {
		response.ReturnError(c, response.PERMISSION_DENIED, "当前登录密码错误")
		return
	}
	if len(input.NewPassword) < 6 || input.NewPassword != input.ConfirmPassword {
		response.ReturnError(c, response.INVALID_ARGUMENT, "新密码至少 6 位且两次输入必须一致")
		return
	}
	if input.NewPassword == input.CurrentPassword {
		response.ReturnError(c, response.INVALID_ARGUMENT, "新密码不能与当前密码相同")
		return
	}
	hash, err := system.HashPassword(input.NewPassword)
	if err != nil {
		response.ReturnError(c, response.INTERNAL, "密码加密失败")
		return
	}
	if err := pgdb.GetClient().Model(&item).Update("password", hash).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "修改登录密码失败")
		return
	}
	response.ReturnData(c, nil)
}
