package finance

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

type rechargeResponse struct {
	ID            uint      `json:"id"`
	RequestID     string    `json:"request_id"`
	CustomerID    uint      `json:"customer_id"`
	CustomerName  string    `json:"customer_name"`
	CustomerPhone string    `json:"customer_phone"`
	Amount        float64   `json:"amount"`
	Currency      string    `json:"currency"`
	Method        string    `json:"method"`
	Voucher       string    `json:"voucher"`
	Status        uint      `json:"status"`
	Remark        string    `json:"remark"`
	FailureReason string    `json:"failure_reason"`
	ReviewedAt    int64     `json:"reviewed_at"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
}
type withdrawalResponse struct {
	system.FinanceWithdrawal
	CustomerName  string `json:"customer_name"`
	CustomerPhone string `json:"customer_phone"`
}

func ListRecharges(c *gin.Context) {
	page, size := middleware.GetPage(c), middleware.GetPageSize(c)
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	db := pgdb.GetClient().Table("finance_recharges").Select("finance_recharges.*, customers.name AS customer_name, customers.phone AS customer_phone").Joins("LEFT JOIN customers ON customers.id = finance_recharges.customer_id").Where("finance_recharges.deleted_at IS NULL")
	if phone := c.Query("phone"); phone != "" {
		db = db.Where("customers.phone LIKE ?", "%"+phone+"%")
	}
	if name := c.Query("name"); name != "" {
		db = db.Where("customers.name LIKE ?", "%"+name+"%")
	}
	if status := c.Query("status"); status != "" {
		db = db.Where("finance_recharges.status = ?", status)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询充值记录失败")
		return
	}
	var rows []rechargeResponse
	if err := db.Order("finance_recharges.id DESC").Offset((page - 1) * size).Limit(size).Scan(&rows).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询充值记录失败")
		return
	}
	response.ReturnData(c, gin.H{"records": rows, "total": total})
}
func ListWithdrawals(c *gin.Context) {
	page, size := middleware.GetPage(c), middleware.GetPageSize(c)
	if page < 1 {
		page = 1
	}
	if size < 1 || size > 100 {
		size = 20
	}
	db := pgdb.GetClient().Table("finance_withdrawals").Select("finance_withdrawals.*, customers.name AS customer_name, customers.phone AS customer_phone").Joins("LEFT JOIN customers ON customers.id = finance_withdrawals.customer_id").Where("finance_withdrawals.deleted_at IS NULL")
	if phone := c.Query("phone"); phone != "" {
		db = db.Where("customers.phone LIKE ?", "%"+phone+"%")
	}
	if name := c.Query("name"); name != "" {
		db = db.Where("customers.name LIKE ?", "%"+name+"%")
	}
	if status := c.Query("status"); status != "" {
		db = db.Where("finance_withdrawals.status = ?", status)
	}
	var total int64
	if err := db.Count(&total).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询提现申请失败")
		return
	}
	var rows []withdrawalResponse
	if err := db.Order("finance_withdrawals.id DESC").Offset((page - 1) * size).Limit(size).Scan(&rows).Error; err != nil {
		response.ReturnError(c, response.DATA_LOSS, "查询提现申请失败")
		return
	}
	response.ReturnData(c, gin.H{"records": rows, "total": total})
}

func SaveRecharge(c *gin.Context) {
	var input system.FinanceRecharge
	if !middleware.CheckParam(&input, c) {
		return
	}
	if input.CustomerID == 0 || input.Amount <= 0 || math.IsNaN(input.Amount) || math.IsInf(input.Amount, 0) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "客户和充值金额为必填项")
		return
	}
	if input.Currency == "" {
		input.Currency = "CNY"
	}
	if input.Method == "" {
		input.Method = "支付宝转账"
	}
	if input.ID == 0 {
		if input.Currency != "CNY" && input.Currency != "HKD" {
			response.ReturnError(c, response.INVALID_ARGUMENT, "充值币种仅支持CNY或HKD")
			return
		}
		if input.Status != 0 && input.Status != system.StatusDisabled {
			response.ReturnError(c, response.INVALID_ARGUMENT, "新增充值记录须先待审核，再执行通过或失败")
			return
		}
		input.Status, input.ReviewedAt, input.FailureReason = system.StatusDisabled, 0, ""
		if input.RequestID == "" {
			input.RequestID = fmt.Sprintf("ADMIN-R-%d", time.Now().UnixNano())
		}
		var count int64
		if err := pgdb.GetClient().Model(&system.Customer{}).Where("id = ?", input.CustomerID).Count(&count).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "读取充值客户失败")
			return
		}
		if count != 1 {
			response.ReturnError(c, response.NOT_FOUND, "充值客户不存在")
			return
		}
		if err := pgdb.GetClient().Create(&input).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "添加充值记录失败")
			return
		}
	} else {
		if err := validateRechargeReview(input.Status, input.FailureReason); err != nil {
			response.ReturnError(c, response.INVALID_ARGUMENT, err.Error())
			return
		}
		err := pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
			current, err := reviewRecharge(tx, input.ID, input.Status, input.FailureReason, input.Remark)
			if err == nil {
				input = current
			}
			return err
		})
		if err != nil {
			response.ReturnError(c, response.DATA_LOSS, err.Error())
			return
		}
	}
	response.ReturnData(c, input)
}

func validateRechargeReview(status uint, reason string) error {
	if status != system.StatusEnabled && status != 3 {
		return fmt.Errorf("请选择通过或失败")
	}
	if status == 3 && strings.TrimSpace(reason) == "" {
		return fmt.Errorf("请填写充值失败原因")
	}
	return nil
}

// 调用方提供事务，锁定充值单和客户，保证并发审核最多入账一次。
func reviewRecharge(tx *gorm.DB, id uint, status uint, reason, remark string) (system.FinanceRecharge, error) {
	var current system.FinanceRecharge
	if err := validateRechargeReview(status, reason); err != nil {
		return current, err
	}
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, id).Error; err != nil {
		return current, err
	}
	if current.Status != system.StatusDisabled {
		return current, fmt.Errorf("充值申请已处理，不能重复审核")
	}
	if current.Currency != "CNY" && current.Currency != "HKD" {
		return current, fmt.Errorf("充值记录币种无效，不能审核")
	}
	if current.Amount <= 0 || math.IsNaN(current.Amount) || math.IsInf(current.Amount, 0) {
		return current, fmt.Errorf("充值记录金额无效，不能审核")
	}
	if status == system.StatusEnabled {
		var customer system.Customer
		if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, current.CustomerID).Error; err != nil {
			return current, err
		}
		field, balance := "balance", customer.Balance
		if current.Currency == "HKD" {
			field, balance = "hkd_balance", customer.HKDBalance
		}
		balance = math.Round((balance+current.Amount)*100) / 100
		if math.IsNaN(balance) || math.IsInf(balance, 0) {
			return current, fmt.Errorf("充值后余额无效，不能审核")
		}
		if err := tx.Model(&customer).Update(field, balance).Error; err != nil {
			return current, err
		}
		if err := tx.Create(&system.CustomerFundRecord{CustomerID: customer.ID, Type: "银转证", Direction: "入账", Currency: current.Currency, Amount: current.Amount, Balance: balance, Remark: "银转证审核入账"}).Error; err != nil {
			return current, err
		}
	}
	current.Status, current.ReviewedAt, current.Remark = status, time.Now().Unix(), strings.TrimSpace(remark)
	current.FailureReason = ""
	if status == 3 {
		current.FailureReason = strings.TrimSpace(reason)
	}
	return current, tx.Save(&current).Error
}

func SaveWithdrawal(c *gin.Context) {
	var input system.FinanceWithdrawal
	if !middleware.CheckParam(&input, c) || input.CustomerID == 0 || input.Amount <= 0 {
		response.ReturnError(c, response.INVALID_ARGUMENT, "客户和提现金额为必填项")
		return
	}
	if input.Currency == "" {
		input.Currency = "CNY"
	}
	if input.Method == "" {
		input.Method = "银行卡"
	}
	if input.ID == 0 {
		if input.Status == 0 {
			input.Status = system.StatusDisabled
		}
		if err := pgdb.GetClient().Create(&input).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "添加提现申请失败")
			return
		}
	} else {
		if input.Status != system.StatusEnabled && input.Status != 3 {
			response.ReturnError(c, response.INVALID_ARGUMENT, "审核状态无效")
			return
		}
		err := pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
			var current system.FinanceWithdrawal
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, input.ID).Error; err != nil {
				return err
			}
			if current.Status != system.StatusDisabled {
				return fmt.Errorf("提现申请已处理")
			}
			var customer system.Customer
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, current.CustomerID).Error; err != nil {
				return err
			}
			if customer.FrozenBalance < current.Amount {
				return fmt.Errorf("客户冻结资金不足")
			}
			customer.FrozenBalance -= current.Amount
			if input.Status == 3 {
				customer.Balance += current.Amount
			}
			if err := tx.Save(&customer).Error; err != nil {
				return err
			}
			if input.Status == system.StatusEnabled {
				if err := tx.Create(&system.CustomerFundRecord{CustomerID: customer.ID, Type: "证转银", Direction: "出账", Currency: current.Currency, Amount: current.Amount, Balance: customer.Balance, Remark: "证转银审核完成"}).Error; err != nil {
					return err
				}
			}
			current.Status, current.ReviewedAt, current.FailureReason, current.Remark = input.Status, time.Now().Unix(), input.FailureReason, input.Remark
			return tx.Save(&current).Error
		})
		if err != nil {
			response.ReturnError(c, response.DATA_LOSS, err.Error())
			return
		}
	}
	response.ReturnData(c, input)
}
