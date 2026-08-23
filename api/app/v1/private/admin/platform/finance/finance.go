package finance

import (
	"fmt"
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
	system.FinanceRecharge
	CustomerName  string `json:"customer_name"`
	CustomerPhone string `json:"customer_phone"`
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
	if !middleware.CheckParam(&input, c) || input.CustomerID == 0 || input.Amount <= 0 {
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
		if input.Status == 0 {
			input.Status = system.StatusDisabled
		}
		if err := pgdb.GetClient().Create(&input).Error; err != nil {
			response.ReturnError(c, response.DATA_LOSS, "添加充值记录失败")
			return
		}
	} else {
		if input.Status != system.StatusEnabled && input.Status != 3 {
			response.ReturnError(c, response.INVALID_ARGUMENT, "审核状态无效")
			return
		}
		err := pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
			var current system.FinanceRecharge
			if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&current, input.ID).Error; err != nil {
				return err
			}
			if current.Status != system.StatusDisabled {
				return fmt.Errorf("充值申请已处理")
			}
			current.Status, current.ReviewedAt, current.FailureReason, current.Remark = input.Status, time.Now().Unix(), input.FailureReason, input.Remark
			if input.Status == system.StatusEnabled {
				var customer system.Customer
				if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, current.CustomerID).Error; err != nil {
					return err
				}
				customer.Balance += current.Amount
				if err := tx.Save(&customer).Error; err != nil {
					return err
				}
				if err := tx.Create(&system.CustomerFundRecord{CustomerID: customer.ID, Type: "银转证", Direction: "入账", Currency: current.Currency, Amount: current.Amount, Balance: customer.Balance, Remark: "银转证审核入账"}).Error; err != nil {
					return err
				}
			}
			return tx.Save(&current).Error
		})
		if err != nil {
			response.ReturnError(c, response.DATA_LOSS, err.Error())
			return
		}
	}
	response.ReturnData(c, input)
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
