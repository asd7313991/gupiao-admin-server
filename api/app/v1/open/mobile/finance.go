package mobile

import (
	"fmt"
	"math"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

const (
	minTransferAmount = 100.0
	maxTransferAmount = 5_000_000.0
)

type transferInput struct {
	Amount   float64 `json:"amount"`
	TradePIN string  `json:"trade_pin"`
	Remark   string  `json:"remark"`
}

type transferRecordView struct {
	ID             uint    `json:"id"`
	RequestID      string  `json:"request_id"`
	Type           string  `json:"type"`
	Amount         float64 `json:"amount"`
	Status         uint    `json:"status"`
	StatusLabel    string  `json:"status_label"`
	BankName       string  `json:"bank_name"`
	BankCardMasked string  `json:"bank_card_masked"`
	Remark         string  `json:"remark"`
	FailureReason  string  `json:"failure_reason"`
	CreatedAt      int64   `json:"created_at"`
	ReviewedAt     int64   `json:"reviewed_at"`
}

type fundFlowView struct {
	ID        uint    `json:"id"`
	Type      string  `json:"type"`
	Direction string  `json:"direction"`
	Currency  string  `json:"currency"`
	Amount    float64 `json:"amount"`
	Balance   float64 `json:"balance"`
	Remark    string  `json:"remark"`
	CreatedAt int64   `json:"created_at"`
}

func FinanceSummary(c *gin.Context) {
	item, ok := currentVerificationCustomer(c)
	if !ok {
		return
	}
	response.ReturnData(c, gin.H{
		"balance": item.Balance, "frozen_balance": item.FrozenBalance,
		"bank_name": item.BankName, "bank_card_masked": mask(item.BankCard, 4, 4),
		"verified": item.Verified, "has_trade_pin": item.TradePassword != "",
	})
}

func RequestRecharge(c *gin.Context) {
	input, ok := bindTransferInput(c)
	if !ok {
		return
	}
	item, ok := currentVerificationCustomer(c)
	if !ok || !validateTransferCustomer(c, item, input.TradePIN) {
		return
	}
	token, err := randomToken(10)
	if err != nil {
		response.ReturnError(c, response.INTERNAL, "生成申请编号失败")
		return
	}
	record := system.FinanceRecharge{RequestID: "R" + token, CustomerID: item.ID, Amount: input.Amount, Currency: "CNY", Method: "银转证", Status: system.StatusDisabled, Remark: input.Remark}
	if err := pgdb.GetClient().Create(&record).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "提交银转证申请失败")
		return
	}
	response.ReturnData(c, transferViewFromRecharge(record, item))
}

func RequestWithdrawal(c *gin.Context) {
	input, ok := bindTransferInput(c)
	if !ok {
		return
	}
	customerID := middleware.GetCurrentCustomerID(c)
	var record system.FinanceWithdrawal
	err := pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
		var item system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&item, customerID).Error; err != nil {
			return err
		}
		if item.Verified != system.StatusEnabled || item.BankCard == "" || item.TradePassword == "" {
			return fmt.Errorf("请先完成实名认证、银行卡和交易密码设置")
		}
		if item.FundStatus != system.StatusEnabled {
			return fmt.Errorf("资金账户不可用")
		}
		if !system.VerifyPassword(input.TradePIN, item.TradePassword, "bcrypt") {
			return fmt.Errorf("交易密码错误")
		}
		if item.Balance < input.Amount {
			return fmt.Errorf("可用余额不足")
		}
		token, err := randomToken(10)
		if err != nil {
			return err
		}
		item.Balance -= input.Amount
		item.FrozenBalance += input.Amount
		if err := tx.Save(&item).Error; err != nil {
			return err
		}
		record = system.FinanceWithdrawal{RequestID: "W" + token, CustomerID: item.ID, Amount: input.Amount, Currency: "CNY", Method: "证转银", BankName: item.BankName, BankCard: item.BankCard, BankAddress: item.BankAddress, Status: system.StatusDisabled, Remark: input.Remark}
		return tx.Create(&record).Error
	})
	if err != nil {
		response.ReturnError(c, response.FAILED_PRECONDITION, err.Error())
		return
	}
	var item system.Customer
	_ = pgdb.GetClient().First(&item, customerID).Error
	response.ReturnData(c, transferViewFromWithdrawal(record, item))
}

func ListCustomerRecharges(c *gin.Context) {
	page, size := transferPage(c)
	db := pgdb.GetClient().Where("customer_id = ? AND deleted_at IS NULL", middleware.GetCurrentCustomerID(c))
	var total int64
	if err := db.Model(&system.FinanceRecharge{}).Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取银转证记录失败")
		return
	}
	var rows []system.FinanceRecharge
	if err := db.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取银转证记录失败")
		return
	}
	result := make([]transferRecordView, len(rows))
	for index, row := range rows {
		result[index] = transferViewFromRecharge(row, system.Customer{})
	}
	response.ReturnData(c, gin.H{"records": result, "total": total, "page": page, "page_size": size})
}

func ListCustomerWithdrawals(c *gin.Context) {
	page, size := transferPage(c)
	db := pgdb.GetClient().Where("customer_id = ? AND deleted_at IS NULL", middleware.GetCurrentCustomerID(c))
	var total int64
	if err := db.Model(&system.FinanceWithdrawal{}).Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取证转银记录失败")
		return
	}
	var rows []system.FinanceWithdrawal
	if err := db.Order("id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取证转银记录失败")
		return
	}
	result := make([]transferRecordView, len(rows))
	for index, row := range rows {
		result[index] = transferViewFromWithdrawal(row, system.Customer{})
	}
	response.ReturnData(c, gin.H{"records": result, "total": total, "page": page, "page_size": size})
}

func ListCustomerFundFlows(c *gin.Context) {
	page, size := transferPage(c)
	db := pgdb.GetClient().Where("customer_id = ?", middleware.GetCurrentCustomerID(c))
	if flowType := c.Query("type"); flowType != "" {
		db = db.Where("type = ?", flowType)
	}
	var total int64
	if err := db.Model(&system.CustomerFundRecord{}).Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取资金流水失败")
		return
	}
	var rows []system.CustomerFundRecord
	if err := db.Order("created_at DESC, id DESC").Offset((page - 1) * size).Limit(size).Find(&rows).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "读取资金流水失败")
		return
	}
	result := make([]fundFlowView, len(rows))
	for index, row := range rows {
		flowType, direction := row.Type, row.Direction
		if flowType == "" {
			flowType = "资金调整"
		}
		if direction == "" {
			if row.Amount < 0 {
				direction = "扣款"
			} else {
				direction = "入账"
			}
		}
		result[index] = fundFlowView{ID: row.ID, Type: flowType, Direction: direction, Currency: row.Currency, Amount: math.Abs(row.Amount), Balance: row.Balance, Remark: row.Remark, CreatedAt: row.CreatedAt.Unix()}
	}
	response.ReturnData(c, gin.H{"records": result, "total": total, "page": page, "page_size": size})
}

func bindTransferInput(c *gin.Context) (transferInput, bool) {
	var input transferInput
	if err := c.ShouldBindJSON(&input); err != nil || input.Amount < minTransferAmount || input.Amount > maxTransferAmount || input.Amount != math.Round(input.Amount*100)/100 || !tradePINPattern.MatchString(input.TradePIN) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "金额须为 100 至 500 万元，且交易密码为 6 位数字")
		return input, false
	}
	return input, true
}

func validateTransferCustomer(c *gin.Context, item system.Customer, tradePIN string) bool {
	if item.Verified != system.StatusEnabled || item.BankCard == "" || item.TradePassword == "" {
		response.ReturnError(c, response.FAILED_PRECONDITION, "请先完成实名认证、银行卡和交易密码设置")
		return false
	}
	if item.FundStatus != system.StatusEnabled {
		response.ReturnError(c, response.FAILED_PRECONDITION, "资金账户不可用")
		return false
	}
	if !system.VerifyPassword(tradePIN, item.TradePassword, "bcrypt") {
		response.ReturnError(c, response.PERMISSION_DENIED, "交易密码错误")
		return false
	}
	return true
}

func transferPage(c *gin.Context) (int, int) {
	page, size := middleware.GetPage(c), middleware.GetPageSize(c)
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	return page, size
}

func transferStatus(status uint) string {
	if status == system.StatusEnabled {
		return "已完成"
	}
	if status == 3 {
		return "已拒绝"
	}
	return "待审核"
}

func transferViewFromRecharge(row system.FinanceRecharge, customer system.Customer) transferRecordView {
	return transferRecordView{ID: row.ID, RequestID: row.RequestID, Type: "银转证", Amount: row.Amount, Status: row.Status, StatusLabel: transferStatus(row.Status), BankName: customer.BankName, BankCardMasked: mask(customer.BankCard, 4, 4), Remark: row.Remark, FailureReason: row.FailureReason, CreatedAt: row.CreatedAt.Unix(), ReviewedAt: row.ReviewedAt}
}

func transferViewFromWithdrawal(row system.FinanceWithdrawal, customer system.Customer) transferRecordView {
	bankName, bankCard := row.BankName, row.BankCard
	if bankName == "" {
		bankName = customer.BankName
	}
	if bankCard == "" {
		bankCard = customer.BankCard
	}
	return transferRecordView{ID: row.ID, RequestID: row.RequestID, Type: "证转银", Amount: row.Amount, Status: row.Status, StatusLabel: transferStatus(row.Status), BankName: bankName, BankCardMasked: mask(bankCard, 4, 4), Remark: row.Remark, FailureReason: row.FailureReason, CreatedAt: row.CreatedAt.Unix(), ReviewedAt: row.ReviewedAt}
}
