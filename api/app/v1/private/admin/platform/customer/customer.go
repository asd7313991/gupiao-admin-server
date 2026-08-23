package customer

import (
	"fmt"
	"strings"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"time"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

type customerResponse struct {
	ID                 uint      `json:"id"`
	Phone              string    `json:"phone"`
	Name               string    `json:"name"`
	IDCard             string    `json:"id_card"`
	BankName           string    `json:"bank_name"`
	BankCard           string    `json:"bank_card"`
	BankAddress        string    `json:"bank_address"`
	GroupName          string    `json:"group_name"`
	Balance            float64   `json:"balance"`
	StrategyBalance    float64   `json:"strategy_balance"`
	FrozenBalance      float64   `json:"frozen_balance"`
	TotalProfit        float64   `json:"total_profit"`
	TotalLoss          float64   `json:"total_loss"`
	Status             uint      `json:"status"`
	FundStatus         uint      `json:"fund_status"`
	Verified           uint      `json:"verified"`
	IDCardFront        string    `json:"id_card_front"`
	IDCardBack         string    `json:"id_card_back"`
	VerificationVideo  string    `json:"verification_video"`
	VerificationRemark string    `json:"verification_remark"`
	Remark             string    `json:"remark"`
	CreatedAt          time.Time `json:"created_at"`
	UpdatedAt          time.Time `json:"updated_at"`
}

func toResponse(customer system.Customer) customerResponse {
	return customerResponse{ID: customer.ID, Phone: customer.Phone, Name: customer.Name, IDCard: customer.IDCard, BankName: customer.BankName, BankCard: customer.BankCard, BankAddress: customer.BankAddress, GroupName: customer.GroupName, Balance: customer.Balance, StrategyBalance: customer.StrategyBalance, FrozenBalance: customer.FrozenBalance, TotalProfit: customer.TotalProfit, TotalLoss: customer.TotalLoss, Status: customer.Status, FundStatus: customer.FundStatus, Verified: customer.Verified, IDCardFront: customer.IDCardFront, IDCardBack: customer.IDCardBack, VerificationVideo: customer.VerificationVideo, VerificationRemark: customer.VerificationRemark, Remark: customer.Remark, CreatedAt: customer.CreatedAt, UpdatedAt: customer.UpdatedAt}
}

func List(c *gin.Context) {
	var query struct {
		Phone, IDCard, Name, Group string
		Status                     uint `form:"status"`
	}
	if !middleware.CheckParam(&query, c) {
		return
	}
	page, pageSize := middleware.GetPage(c), middleware.GetPageSize(c)
	db := pgdb.GetClient().Model(&system.Customer{}).Where("deleted_at IS NULL")
	if query.Phone != "" {
		db = db.Where("phone LIKE ?", "%"+query.Phone+"%")
	}
	if query.IDCard != "" {
		db = db.Where("id_card LIKE ?", "%"+query.IDCard+"%")
	}
	if query.Name != "" {
		db = db.Where("name LIKE ?", "%"+query.Name+"%")
	}
	if query.Group != "" {
		db = db.Where("group_name = ?", query.Group)
	}
	if query.Status != 0 {
		db = db.Where("status = ?", query.Status)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询客户失败")
		return
	}
	var items []system.Customer
	if err := db.Order("id DESC").Offset((page - 1) * pageSize).Limit(pageSize).Find(&items).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询客户失败")
		return
	}
	result := make([]customerResponse, len(items))
	for index, item := range items {
		result[index] = toResponse(item)
	}
	response.ReturnData(c, gin.H{
		"records": result,
		"total":   total,
	})
}

func Detail(c *gin.Context) {
	customer, ok := getCustomer(c)
	if ok {
		response.ReturnData(c, toResponse(customer))
	}
}

func Create(c *gin.Context) {
	var customer system.Customer
	if !middleware.CheckParam(&customer, c) {
		return
	}
	if customer.Phone == "" || customer.Name == "" || customer.IDCard == "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "手机号、姓名和身份证号为必填项")
		return
	}
	customer.Status, customer.FundStatus = enabled(customer.Status), enabled(customer.FundStatus)
	if customer.GroupName == "" {
		customer.GroupName = "内部"
	}
	if err := pgdb.GetClient().Create(&customer).Error; err != nil {
		response.ReturnError(c, response.ALREADY_EXISTS, "手机号已存在")
		return
	}
	response.ReturnData(c, toResponse(customer))
}

func Update(c *gin.Context) {
	var input system.Customer
	if !middleware.CheckParam(&input, c) {
		return
	}
	var current system.Customer
	if input.ID == 0 || pgdb.GetClient().First(&current, input.ID).Error != nil {
		response.ReturnError(c, response.NOT_FOUND, "客户不存在")
		return
	}
	input.CreatedAt = current.CreatedAt
	if err := pgdb.GetClient().Omit("password", "trade_password", "balance", "strategy_balance", "frozen_balance", "total_profit", "total_loss").Save(&input).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "更新客户失败")
		return
	}
	response.ReturnData(c, toResponse(input))
}

func Deposit(c *gin.Context) {
	var input struct {
		ID       uint    `json:"id" binding:"required"`
		Currency string  `json:"currency"`
		Amount   float64 `json:"amount"`
		Remark   string  `json:"remark"`
	}
	if !middleware.CheckParam(&input, c) || input.Amount == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "入账金额不能为零")
		return
	}
	err := pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
		var customer system.Customer
		if err := tx.First(&customer, input.ID).Error; err != nil {
			return err
		}
		if customer.FundStatus != system.StatusEnabled {
			return fmt.Errorf("资金账户已锁定")
		}
		customer.Balance += input.Amount
		if customer.Balance < 0 {
			return fmt.Errorf("余额不足")
		}
		if err := tx.Save(&customer).Error; err != nil {
			return err
		}
		return tx.Create(&system.CustomerFundRecord{CustomerID: customer.ID, Currency: input.Currency, Amount: input.Amount, Balance: customer.Balance, Remark: input.Remark}).Error
	})
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, err.Error())
		return
	}
	response.ReturnData(c, nil)
}

func SetStatus(c *gin.Context)     { updateFlag(c, "status") }
func SetFundStatus(c *gin.Context) { updateFlag(c, "fund_status") }

func UpdatePassword(c *gin.Context) {
	var input struct {
		ID       uint   `json:"id"`
		Password string `json:"password"`
		Type     string `json:"type"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 || len(input.Password) < 6 || (input.Type != "login" && input.Type != "trade") {
		response.ReturnError(c, response.INVALID_ARGUMENT, "密码至少 6 位，且密码类型必须有效")
		return
	}
	hash, err := system.HashPassword(input.Password)
	if err != nil {
		response.ReturnError(c, response.INTERNAL, "密码加密失败")
		return
	}
	field := "password"
	if input.Type == "trade" {
		field = "trade_password"
	}
	if err := pgdb.GetClient().Model(&system.Customer{}).Where("id = ?", input.ID).Update(field, hash).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "修改密码失败")
		return
	}
	response.ReturnData(c, nil)
}

func UpdateBank(c *gin.Context) {
	var input struct {
		ID          uint   `json:"id"`
		BankName    string `json:"bank_name"`
		BankCard    string `json:"bank_card"`
		BankAddress string `json:"bank_address"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 || input.BankName == "" || input.BankCard == "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "客户、开户行和银行卡号为必填项")
		return
	}
	if err := pgdb.GetClient().Model(&system.Customer{}).Where("id = ?", input.ID).Updates(map[string]any{"bank_name": input.BankName, "bank_card": input.BankCard, "bank_address": input.BankAddress}).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "更新银行卡失败")
		return
	}
	response.ReturnData(c, nil)
}

func ReviewVerification(c *gin.Context) {
	var input struct {
		ID       uint   `json:"id"`
		Verified uint   `json:"verified"`
		Remark   string `json:"remark"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 || (input.Verified != 1 && input.Verified != 3) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "审核参数无效")
		return
	}
	if input.Verified == 3 && input.Remark == "" {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请填写驳回原因")
		return
	}
	if err := pgdb.GetClient().Model(&system.Customer{}).Where("id = ?", input.ID).Updates(map[string]any{"verified": input.Verified, "verification_remark": input.Remark}).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "认证审核失败")
		return
	}
	response.ReturnData(c, nil)
}

func BatchReviewVerification(c *gin.Context) {
	var input struct {
		IDs    []uint `json:"ids"`
		Remark string `json:"remark"`
	}
	if !middleware.CheckParam(&input, c) || len(input.IDs) == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请选择需要审核的客户")
		return
	}
	if err := pgdb.GetClient().Model(&system.Customer{}).Where("id IN ?", input.IDs).Updates(map[string]any{"verified": uint(1), "verification_remark": input.Remark}).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "批量审核失败")
		return
	}
	response.ReturnData(c, nil)
}

func BatchSetStatus(c *gin.Context) {
	var input struct {
		IDs    []uint `json:"ids"`
		Status uint   `json:"status"`
	}
	if !middleware.CheckParam(&input, c) || len(input.IDs) == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请选择客户")
		return
	}
	if input.Status != system.StatusEnabled {
		input.Status = system.StatusDisabled
	}
	if err := pgdb.GetClient().Model(&system.Customer{}).Where("id IN ?", input.IDs).Update("status", input.Status).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "批量更新账户状态失败")
		return
	}
	response.ReturnData(c, nil)
}

func FundRecords(c *gin.Context) {
	var records []system.CustomerFundRecord
	db := pgdb.GetClient().Order("id DESC")
	if phone := c.Query("phone"); phone != "" {
		db = db.Joins("JOIN customers ON customers.id = customer_fund_records.customer_id").Where("customers.phone LIKE ?", "%"+phone+"%")
	}
	if err := db.Find(&records).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询资金流水失败")
		return
	}
	response.ReturnData(c, records)
}

func Devices(c *gin.Context) {
	var records []system.CustomerDevice
	db := pgdb.GetClient().Order("id DESC")
	if phone := c.Query("phone"); phone != "" {
		db = db.Joins("JOIN customers ON customers.id = customer_devices.customer_id").Where("customers.phone LIKE ?", "%"+phone+"%")
	}
	if err := db.Find(&records).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询设备失败")
		return
	}
	response.ReturnData(c, records)
}

func SetDeviceBlocked(c *gin.Context) {
	var input struct {
		ID      uint `json:"id"`
		Blocked uint `json:"blocked"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "设备 ID 无效")
		return
	}
	if input.Blocked != system.StatusEnabled {
		input.Blocked = system.StatusDisabled
	}
	if err := pgdb.GetClient().Model(&system.CustomerDevice{}).Where("id = ?", input.ID).Update("blocked", input.Blocked).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "更新设备状态失败")
		return
	}
	response.ReturnData(c, nil)
}

func BatchSetDeviceBlocked(c *gin.Context) {
	var input struct {
		Phone      string `json:"phone"`
		DeviceID   string `json:"device_id"`
		APIBaseURL string `json:"api_base_url"`
		Blocked    uint   `json:"blocked"`
	}
	if !middleware.CheckParam(&input, c) || (strings.TrimSpace(input.Phone) == "" && strings.TrimSpace(input.DeviceID) == "" && strings.TrimSpace(input.APIBaseURL) == "") {
		response.ReturnError(c, response.INVALID_ARGUMENT, "请至少填写一个匹配条件")
		return
	}
	if input.Blocked != system.StatusEnabled {
		input.Blocked = system.StatusDisabled
	}
	db := pgdb.GetClient().Model(&system.CustomerDevice{})
	conditions := make([]string, 0, 3)
	args := make([]any, 0, 3)
	if input.DeviceID != "" {
		conditions = append(conditions, "customer_devices.device_id = ?")
		args = append(args, input.DeviceID)
	}
	if input.APIBaseURL != "" {
		conditions = append(conditions, "customer_devices.api_base_url = ?")
		args = append(args, input.APIBaseURL)
	}
	if input.Phone != "" {
		db = db.Joins("JOIN customers ON customers.id = customer_devices.customer_id")
		conditions = append(conditions, "customers.phone = ?")
		args = append(args, input.Phone)
	}
	result := db.Where(strings.Join(conditions, " OR "), args...).Update("blocked", input.Blocked)
	if result.Error != nil {
		response.ReturnError(c, response.DATA_LOSS, "批量更新设备状态失败")
		return
	}
	response.ReturnData(c, gin.H{"updated": result.RowsAffected})
}
func Delete(c *gin.Context) {
	customer, ok := getCustomer(c)
	if ok {
		if err := pgdb.GetClient().Delete(&customer).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "删除客户失败")
			return
		}
		response.ReturnData(c, nil)
	}
}

func getCustomer(c *gin.Context) (system.Customer, bool) {
	id := uint(0)
	fmt.Sscan(c.Query("id"), &id)
	var customer system.Customer
	if id == 0 || pgdb.GetClient().First(&customer, id).Error != nil {
		response.ReturnError(c, response.NOT_FOUND, "客户不存在")
		return customer, false
	}
	return customer, true
}
func updateFlag(c *gin.Context, field string) {
	var input struct {
		ID     uint `json:"id"`
		Status uint `json:"status"`
	}
	if !middleware.CheckParam(&input, c) || input.ID == 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "客户 ID 无效")
		return
	}
	var customer system.Customer
	if pgdb.GetClient().First(&customer, input.ID).Error != nil {
		response.ReturnError(c, response.NOT_FOUND, "客户不存在")
		return
	}
	if input.Status != system.StatusEnabled {
		input.Status = system.StatusDisabled
	}
	if err := pgdb.GetClient().Model(&customer).Update(field, input.Status).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "更新状态失败")
		return
	}
	response.ReturnData(c, nil)
}
func enabled(status uint) uint {
	if status == 0 {
		return system.StatusEnabled
	}
	return status
}
