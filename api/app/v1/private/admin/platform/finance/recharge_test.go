package finance

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"api-server/api/response"
	"api-server/db/pgdb/system"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func TestRechargeListResponseFields(t *testing.T) {
	var row rechargeResponse
	row.ID, row.CustomerID, row.Amount = 12, 45, 1234.56
	row.Currency, row.Method, row.Status = "CNY", "银转证", 2
	row.RequestID, row.CustomerName, row.CustomerPhone = "R-test", "测试充值客户", "13900000001"
	row.CreatedAt = time.Unix(1793170000, 0)
	assertRechargeResponse(t, row)
}

func assertRechargeResponse(t *testing.T, row rechargeResponse) {
	t.Helper()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	response.ReturnData(context, gin.H{"records": []rechargeResponse{row}, "total": 1})
	var result struct {
		Code int `json:"code"`
		Data struct {
			Records []map[string]any `json:"records"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if result.Code != 200 || len(result.Data.Records) != 1 {
		t.Fatalf("充值列表响应无效：%s", recorder.Body.String())
	}
	fields := result.Data.Records[0]
	for name, expected := range map[string]any{
		"id": float64(row.ID), "customer_id": float64(row.CustomerID), "amount": row.Amount,
		"currency": row.Currency, "method": row.Method, "status": float64(row.Status),
		"request_id": row.RequestID, "customer_name": row.CustomerName, "customer_phone": row.CustomerPhone,
		"created_at": float64(row.CreatedAt.Unix()), "reviewed_at": float64(row.ReviewedAt),
	} {
		if fields[name] != expected {
			t.Errorf("列表顶层字段%s应为%v，实际%v；完整响应%s", name, expected, fields[name], recorder.Body.String())
		}
	}
	if _, nested := fields["FinanceRecharge"]; nested {
		t.Error("充值字段不能嵌套在FinanceRecharge内")
	}
}

func TestSaveRechargeRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		body, message string
	}{
		{`{"customer_id":1,"amount":10,"status":1}`, "新增充值记录须先待审核"},
		{`{"customer_id":1,"amount":10,"status":3}`, "新增充值记录须先待审核"},
		{`{"customer_id":1,"amount":10,"currency":"USD"}`, "充值币种仅支持"},
		{`{"id":1,"customer_id":1,"amount":10,"status":2}`, "请选择通过或失败"},
		{`{"id":1,"customer_id":1,"amount":10,"status":3,"failure_reason":" "}`, "请填写充值失败原因"},
		{`{"customer_id":1,"amount":-1}`, "客户和充值金额为必填项"},
	} {
		recorder := httptest.NewRecorder()
		context, _ := gin.CreateTestContext(recorder)
		context.Request = httptest.NewRequest(http.MethodPost, "/", strings.NewReader(tc.body))
		context.Request.Header.Set("Content-Type", "application/json")
		SaveRecharge(context)
		var result struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
			t.Fatal(err)
		}
		if result.Code == 200 || !strings.Contains(result.Message, tc.message) {
			t.Fatalf("应拒绝%s，实际%s", tc.body, recorder.Body.String())
		}
	}
}

func TestValidateRechargeReview(t *testing.T) {
	for _, tc := range []struct {
		status uint
		reason string
		valid  bool
	}{{1, "", true}, {3, "未收到款项", true}, {3, "", false}, {3, " \n ", false}, {2, "", false}, {0, "", false}, {4, "", false}} {
		if err := validateRechargeReview(tc.status, tc.reason); (err == nil) != tc.valid {
			t.Fatalf("状态%d、原因%q校验不符：%v", tc.status, tc.reason, err)
		}
	}
}

func TestRechargeReviewIntegration(t *testing.T) {
	dsn := os.Getenv("FINANCE_TEST_DSN")
	if dsn == "" {
		t.Skip("未设置FINANCE_TEST_DSN")
	}
	root, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	rootSQL, err := root.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer rootSQL.Close()
	schema := fmt.Sprintf("recharge_test_%d", time.Now().UnixNano())
	if err := root.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := root.Exec("DROP SCHEMA " + schema + " CASCADE").Error; err != nil {
			t.Error(err)
		}
	}()
	db, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	defer sqlDB.Close()
	if err := db.AutoMigrate(&system.Customer{}, &system.FinanceRecharge{}, &system.CustomerFundRecord{}); err != nil {
		t.Fatal(err)
	}
	customer := system.Customer{Phone: "19900000888", Balance: 100, HKDBalance: 200, FrozenBalance: 30}
	if err := db.Create(&customer).Error; err != nil {
		t.Fatal(err)
	}
	create := func(currency string, amount float64) system.FinanceRecharge {
		t.Helper()
		record := system.FinanceRecharge{RequestID: fmt.Sprintf("test-%d", time.Now().UnixNano()), CustomerID: customer.ID, Currency: currency, Amount: amount, Status: 2, Remark: "original"}
		if err := db.Create(&record).Error; err != nil {
			t.Fatal(err)
		}
		return record
	}
	review := func(id uint, status uint, reason string) (system.FinanceRecharge, error) {
		var result system.FinanceRecharge
		err := db.Transaction(func(tx *gorm.DB) error {
			var err error
			result, err = reviewRecharge(tx, id, status, reason, "人工审核")
			return err
		})
		return result, err
	}
	assertState := func(cny, hkd float64, count int64) {
		t.Helper()
		var actual system.Customer
		if err := db.First(&actual, customer.ID).Error; err != nil {
			t.Fatal(err)
		}
		var records int64
		if err := db.Model(&system.CustomerFundRecord{}).Count(&records).Error; err != nil {
			t.Fatal(err)
		}
		if actual.Balance != cny || actual.HKDBalance != hkd || actual.FrozenBalance != 30 || records != count {
			t.Fatalf("余额或流水不符：CNY=%v HKD=%v 冻结=%v 流水=%d", actual.Balance, actual.HKDBalance, actual.FrozenBalance, records)
		}
	}
	cny := create("CNY", 12.34)
	var listed []rechargeResponse
	if err := db.Table("finance_recharges").Select("finance_recharges.*, customers.name AS customer_name, customers.phone AS customer_phone").
		Joins("LEFT JOIN customers ON customers.id = finance_recharges.customer_id").
		Where("finance_recharges.id = ?", cny.ID).Scan(&listed).Error; err != nil {
		t.Fatal(err)
	}
	if len(listed) != 1 || listed[0].Amount != 12.34 || listed[0].ID != cny.ID || listed[0].CustomerID != customer.ID || listed[0].Status != 2 || listed[0].Currency != "CNY" || listed[0].CustomerPhone != customer.Phone || listed[0].CreatedAt.IsZero() {
		t.Fatalf("数据库充值列表字段缺失或错误：%+v", listed)
	}
	assertRechargeResponse(t, listed[0])
	var wg sync.WaitGroup
	results := make(chan error, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); _, err := review(cny.ID, 1, ""); results <- err }()
	}
	wg.Wait()
	close(results)
	success := 0
	for err := range results {
		if err == nil {
			success++
		}
	}
	if success != 1 {
		t.Fatalf("并发审核只能成功一次，实际%d", success)
	}
	assertState(112.34, 200, 1)
	if _, err := review(cny.ID, 3, "改成失败"); err == nil {
		t.Fatal("已通过单不能改为失败")
	}
	hkd := create("HKD", 10.01)
	approved, err := review(hkd.ID, 1, "不应保留的失败原因")
	if err != nil || approved.Status != 1 || approved.ReviewedAt <= 0 || approved.FailureReason != "" {
		t.Fatalf("审核返回结果无效：%+v %v", approved, err)
	}
	assertState(112.34, 210.01, 2)
	var flow system.CustomerFundRecord
	if err := db.Where("currency = ?", "HKD").First(&flow).Error; err != nil || flow.Amount != 10.01 || flow.Balance != 210.01 || flow.Direction != "入账" {
		t.Fatalf("港币流水无效：%+v %v", flow, err)
	}
	failed := create("CNY", 99)
	if _, err := review(failed.ID, 3, " \n "); err == nil {
		t.Fatal("失败必须填写原因")
	}
	rejected, err := review(failed.ID, 3, " 未收到款项 ")
	if err != nil || rejected.Status != 3 || rejected.FailureReason != "未收到款项" || rejected.ReviewedAt <= 0 {
		t.Fatalf("失败审核无效：%+v %v", rejected, err)
	}
	if _, err := review(failed.ID, 1, ""); err == nil {
		t.Fatal("已失败单不能重新通过")
	}
	assertState(112.34, 210.01, 2)
	for _, record := range []system.FinanceRecharge{create("USD", 20), create("CNY", -1)} {
		if _, err := review(record.ID, 1, ""); err == nil {
			t.Fatal("币种或金额无效不应入账")
		}
	}
	rollback := create("CNY", 30)
	const callback = "test:fail_recharge_fund_record"
	if err := db.Callback().Create().Before("gorm:create").Register(callback, func(tx *gorm.DB) {
		if tx.Statement.Schema != nil && tx.Statement.Schema.Table == "customer_fund_records" {
			tx.AddError(fmt.Errorf("模拟流水保存失败"))
		}
	}); err != nil {
		t.Fatal(err)
	}
	defer db.Callback().Create().Remove(callback)
	if _, err := review(rollback.ID, 1, ""); err == nil {
		t.Fatal("流水失败时应返回错误")
	}
	var unchanged system.FinanceRecharge
	if err := db.First(&unchanged, rollback.ID).Error; err != nil {
		t.Fatal(err)
	}
	if unchanged.Status != 2 || unchanged.ReviewedAt != 0 {
		t.Fatal("失败事务不应改变审核状态")
	}
	assertState(112.34, 210.01, 2)
}
