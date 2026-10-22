package mobile

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/big"
	"regexp"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"api-server/api/middleware"
	"api-server/api/response"
	"api-server/db/pgdb"
	"api-server/db/pgdb/system"
)

type exchangeInput struct {
	RequestID string  `json:"request_id"`
	From      string  `json:"from_currency"`
	Amount    float64 `json:"amount"`
	Rate      float64 `json:"rate"`
	TradePIN  string  `json:"trade_pin"`
}

var exchangeRequestPattern = regexp.MustCompile(`^[a-zA-Z0-9-]{16,64}$`)

type exchangeRejection string

func (err exchangeRejection) Error() string { return string(err) }

func validExchangeRate(rate float64) bool {
	return !math.IsNaN(rate) && !math.IsInf(rate, 0) && rate >= 0.00000001 && rate <= 100 &&
		math.Abs(rate*1e8-math.Round(rate*1e8)) < 0.000001
}

func exchangeRateFromConfig(config string) (float64, error) {
	var settings mobileTradeSettings
	if err := json.Unmarshal([]byte(config), &settings); err != nil || !validExchangeRate(settings.Links.HKDRate) {
		return 0, fmt.Errorf("港币汇率未正确配置，请联系管理员")
	}
	return settings.Links.HKDRate, nil
}

func exchangeReceived(amount, rate float64, from string) (float64, error) {
	if !validExchangeRate(rate) || math.IsNaN(amount) || math.IsInf(amount, 0) || amount < 0.01 || amount > maxTransferAmount ||
		math.Abs(amount*100-math.Round(amount*100)) > 0.000001 {
		return 0, fmt.Errorf("兑换金额须为 0.01 至 500 万，最多两位小数，且汇率必须有效")
	}
	value, ok := new(big.Rat).SetString(fmt.Sprintf("%.2f", amount))
	if !ok {
		return 0, fmt.Errorf("兑换金额无效")
	}
	ratio, ok := new(big.Rat).SetString(fmt.Sprintf("%.8f", rate))
	if !ok || ratio.Sign() <= 0 {
		return 0, fmt.Errorf("兑换汇率无效")
	}
	switch from {
	case "CNY":
		value.Quo(value, ratio)
	case "HKD":
		value.Mul(value, ratio)
	default:
		return 0, fmt.Errorf("兑换方向无效")
	}
	// 兑入金额向下保留到分，避免往返兑换因四舍五入产生额外资金。
	value.Mul(value, big.NewRat(100, 1))
	cents := new(big.Int).Quo(value.Num(), value.Denom())
	if !cents.IsInt64() || cents.Sign() <= 0 || cents.Int64() > 1_000_000_000_00 {
		return 0, fmt.Errorf("兑入金额过小或超过账户限制")
	}
	return float64(cents.Int64()) / 100, nil
}

func ExchangeSummary(c *gin.Context) {
	customer, ok := currentVerificationCustomer(c)
	if !ok {
		return
	}
	var row system.AppSystemSetting
	if err := pgdb.GetClient().First(&row).Error; err != nil {
		zap.L().Error("读取兑换配置失败", zap.Error(err))
		response.ReturnError(c, response.DATA_LOSS, "读取兑换配置失败")
		return
	}
	rate, err := exchangeRateFromConfig(row.Config)
	if err != nil {
		response.ReturnError(c, response.FAILED_PRECONDITION, err.Error())
		return
	}
	response.ReturnData(c, gin.H{
		"cny_balance": customer.Balance, "hkd_balance": customer.HKDBalance,
		"rate": rate, "rate_updated_at": row.UpdatedAt.Unix(),
		"has_trade_pin": customer.TradePassword != "",
	})
}

func ExchangeCurrency(c *gin.Context) {
	var input exchangeInput
	if err := c.ShouldBindJSON(&input); err != nil || !exchangeRequestPattern.MatchString(input.RequestID) {
		response.ReturnError(c, response.INVALID_ARGUMENT, "兑换请求参数或请求编号无效")
		return
	}
	if _, err := exchangeReceived(input.Amount, input.Rate, input.From); err != nil {
		response.ReturnError(c, response.INVALID_ARGUMENT, err.Error())
		return
	}
	var record system.CurrencyExchange
	err := pgdb.GetClient().Transaction(func(tx *gorm.DB) error {
		var err error
		record, err = executeCurrencyExchange(tx, middleware.GetCurrentCustomerID(c), input)
		return err
	})
	var rejection exchangeRejection
	if errors.As(err, &rejection) {
		response.ReturnError(c, response.FAILED_PRECONDITION, rejection.Error())
		return
	}
	if errors.Is(err, errInsufficientExchangeBalance) {
		response.ReturnError(c, response.FAILED_PRECONDITION, err.Error())
		return
	}
	if err != nil {
		zap.L().Error("资金兑换失败", zap.Error(err))
		response.ReturnError(c, response.DATA_LOSS, "资金兑换失败，请使用原请求重试或查看资金明细")
		return
	}
	response.ReturnData(c, record)
}

// 调用方必须在数据库事务内执行，余额、成交记录和两条流水共同提交。
func executeCurrencyExchange(tx *gorm.DB, customerID uint, input exchangeInput) (system.CurrencyExchange, error) {
	var customer system.Customer
	var record system.CurrencyExchange
	if err := tx.Clauses(clause.Locking{Strength: "UPDATE"}).First(&customer, customerID).Error; err != nil {
		return record, err
	}
	if customer.Status != system.StatusEnabled || customer.FundStatus != system.StatusEnabled || customer.Verified != system.StatusEnabled {
		return record, exchangeRejection("账户未完成实名认证或资金账户不可用")
	}
	if customer.TradePassword == "" || !system.VerifyPassword(input.TradePIN, customer.TradePassword, "bcrypt") {
		return record, exchangeRejection("请设置并输入正确的交易密码")
	}
	found := tx.Where("customer_id = ? AND request_id = ?", customer.ID, input.RequestID).First(&record).Error
	if found == nil {
		if record.From != input.From || record.Amount != input.Amount || record.Rate != input.Rate {
			return record, exchangeRejection("请求编号已用于另一笔兑换，请重新发起")
		}
		return record, nil
	}
	if !errors.Is(found, gorm.ErrRecordNotFound) {
		return record, found
	}
	var setting system.AppSystemSetting
	if err := tx.Clauses(clause.Locking{Strength: "SHARE"}).First(&setting).Error; err != nil {
		return record, err
	}
	rate, err := exchangeRateFromConfig(setting.Config)
	if err != nil {
		return record, exchangeRejection(err.Error())
	}
	if input.Rate != rate {
		return record, exchangeRejection("汇率已更新，请刷新汇率后重新确认")
	}
	return applyCurrencyExchange(tx, &customer, input, rate)
}

var errInsufficientExchangeBalance = errors.New("可兑换余额不足")

func applyCurrencyExchange(tx *gorm.DB, customer *system.Customer, input exchangeInput, rate float64) (system.CurrencyExchange, error) {
	received, err := exchangeReceived(input.Amount, rate, input.From)
	if err != nil {
		return system.CurrencyExchange{}, err
	}
	fromBalance, toBalance, to := &customer.Balance, &customer.HKDBalance, "HKD"
	if input.From == "HKD" {
		fromBalance, toBalance, to = &customer.HKDBalance, &customer.Balance, "CNY"
	}
	if *fromBalance < input.Amount {
		return system.CurrencyExchange{}, errInsufficientExchangeBalance
	}
	*fromBalance, *toBalance = roundMoney(*fromBalance-input.Amount), roundMoney(*toBalance+received)
	record := system.CurrencyExchange{CustomerID: customer.ID, RequestID: input.RequestID, From: input.From, To: to,
		Amount: input.Amount, Received: received, Rate: rate, CNYBalance: customer.Balance, HKDBalance: customer.HKDBalance}
	if err := tx.Model(customer).Updates(map[string]any{"balance": customer.Balance, "hkd_balance": customer.HKDBalance}).Error; err != nil {
		return record, err
	}
	if err := tx.Create(&record).Error; err != nil {
		return record, err
	}
	remark := fmt.Sprintf("兑换编号 %s：%.2f %s → %.2f %s；1 HKD = %.8f CNY", input.RequestID, input.Amount, input.From, received, to, rate)
	flows := []system.CustomerFundRecord{
		{CustomerID: customer.ID, Type: "资金兑换", Direction: "扣款", Currency: input.From, Amount: -input.Amount, Balance: *fromBalance, Remark: remark},
		{CustomerID: customer.ID, Type: "资金兑换", Direction: "入账", Currency: to, Amount: received, Balance: *toBalance, Remark: remark},
	}
	return record, tx.Create(&flows).Error
}
