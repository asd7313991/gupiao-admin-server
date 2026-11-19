package customer

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"api-server/api/response"
	"api-server/db/pgdb/system"
	"github.com/gin-gonic/gin"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

func verificationTestDB(t *testing.T, customers []*system.Customer, failUpdate bool) (*gorm.DB, *[]clause.Where) {
	t.Helper()
	db, err := gorm.Open(postgres.Open("host=127.0.0.1 user=test dbname=test sslmode=disable"), &gorm.Config{
		DryRun: true, DisableAutomaticPing: true, SkipDefaultTransaction: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	var writes []clause.Where
	if err := db.Callback().Query().Replace("gorm:query", func(tx *gorm.DB) {
		switch dest := tx.Statement.Dest.(type) {
		case *system.Customer:
			if len(customers) == 0 {
				tx.AddError(gorm.ErrRecordNotFound)
				return
			}
			*dest = *customers[0]
			tx.RowsAffected = 1
		case *[]system.Customer:
			for _, customer := range customers {
				*dest = append(*dest, *customer)
			}
			tx.RowsAffected = int64(len(customers))
		default:
			t.Fatalf("未预期的查询类型：%T", dest)
		}
	}); err != nil {
		t.Fatal(err)
	}
	if err := db.Callback().Update().Replace("gorm:update", func(tx *gorm.DB) {
		if failUpdate {
			tx.AddError(errors.New("模拟审核写入失败"))
			return
		}
		where, ok := tx.Statement.Clauses["WHERE"].Expression.(clause.Where)
		if !ok {
			t.Fatal("审核更新必须限定目标客户")
		}
		writes = append(writes, where)
		updates, ok := tx.Statement.Dest.(map[string]any)
		if !ok || len(updates) != 2 {
			t.Fatalf("审核只应更新认证状态和备注：%v", tx.Statement.Dest)
		}
		for _, customer := range customers {
			customer.Verified = updates["verified"].(uint)
			customer.VerificationRemark = updates["verification_remark"].(string)
		}
		tx.RowsAffected = int64(len(customers))
	}); err != nil {
		t.Fatal(err)
	}
	return db, &writes
}

func verificationRequest(t *testing.T, body string, handler gin.HandlerFunc) (int, string) {
	t.Helper()
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	context.Request = httptest.NewRequest(http.MethodPut, "/", strings.NewReader(body))
	context.Request.Header.Set("Content-Type", "application/json")
	handler(context)
	var result struct {
		Code    int    `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return result.Code, result.Message
}

func TestManualReviewWithoutMaterials(t *testing.T) {
	for _, batch := range []bool{false, true} {
		name := "单人通过"
		if batch {
			name = "批量通过"
		}
		t.Run(name, func(t *testing.T) {
			customers := []*system.Customer{{Model: gorm.Model{ID: 7}, Verified: system.StatusDisabled, Status: system.StatusDisabled}}
			body := `{"id":7,"verified":1,"remark":"后台人工通过"}`
			if batch {
				customers = append(customers, &system.Customer{Model: gorm.Model{ID: 8}, Verified: 3})
				body = `{"ids":[7,8],"remark":"后台人工通过"}`
			}
			db, writes := verificationTestDB(t, customers, false)
			code, message := verificationRequest(t, body, func(c *gin.Context) {
				if batch {
					batchReviewVerification(c, func() *gorm.DB { return db })
				} else {
					reviewVerification(c, func() *gorm.DB { return db })
				}
			})
			if code != 200 || len(*writes) != 1 {
				t.Fatalf("无照片、无姓名号码也应允许后台人工通过：code=%d message=%s writes=%v", code, message, *writes)
			}
			expr := (*writes)[0].Exprs[0].(clause.Expr)
			if batch {
				ids := expr.Vars[0].([]uint)
				if expr.SQL != "id IN ?" || len(ids) != 2 || ids[0] != 7 || ids[1] != 8 {
					t.Fatalf("批量审核目标错误：%+v", expr)
				}
			} else if expr.SQL != "id = ?" || expr.Vars[0] != uint(7) {
				t.Fatalf("单人审核目标错误：%+v", expr)
			}
			for _, customer := range customers {
				if customer.Verified != 1 || customer.VerificationRemark != "后台人工通过" || toResponse(*customer).Verified != 1 {
					t.Fatalf("审核结果和客户列表必须一致：%+v", customer)
				}
				if customer.IDCardFront != "" || customer.IDCardBack != "" || customer.VerificationFace != "" {
					t.Fatal("人工审核不能伪造认证材料")
				}
			}
			if customers[0].Status != system.StatusDisabled {
				t.Fatal("实名认证不能修改账户禁用状态")
			}
		})
	}
}

func TestReviewVerificationInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		body  string
		batch bool
	}{
		{`{"id":0,"verified":1}`, false},
		{`{"id":7,"verified":2}`, false},
		{`{"id":7,"verified":3}`, false},
		{`{"ids":[]}`, true},
	} {
		db := func() *gorm.DB { t.Fatal("无效输入不能访问数据库"); return nil }
		code, _ := verificationRequest(t, tc.body, func(c *gin.Context) {
			if tc.batch {
				batchReviewVerification(c, db)
			} else {
				reviewVerification(c, db)
			}
		})
		if code != response.INVALID_ARGUMENT.Code {
			t.Fatalf("无效审核参数必须拒绝：%s code=%d", tc.body, code)
		}
	}
}

func TestReviewVerificationFailureDoesNotApprove(t *testing.T) {
	for _, tc := range []struct {
		name       string
		batch      bool
		missing    bool
		failUpdate bool
		code       int
	}{
		{"单人不存在", false, true, false, response.NOT_FOUND.Code},
		{"批量部分不存在", true, true, false, response.NOT_FOUND.Code},
		{"单人写入失败", false, false, true, response.DATA_LOSS.Code},
		{"批量写入失败", true, false, true, response.DATA_LOSS.Code},
	} {
		t.Run(tc.name, func(t *testing.T) {
			customer := &system.Customer{Model: gorm.Model{ID: 7}, Verified: 2}
			rows := []*system.Customer{customer}
			if tc.missing && !tc.batch {
				rows = nil
			}
			if tc.batch && tc.failUpdate {
				rows = append(rows, &system.Customer{Model: gorm.Model{ID: 8}})
			}
			db, writes := verificationTestDB(t, rows, tc.failUpdate)
			code, _ := verificationRequest(t, `{"id":7,"verified":1,"ids":[7,8]}`, func(c *gin.Context) {
				if tc.batch {
					batchReviewVerification(c, func() *gorm.DB { return db })
				} else {
					reviewVerification(c, func() *gorm.DB { return db })
				}
			})
			if code != tc.code || len(*writes) != 0 || customer.Verified != 2 {
				t.Fatalf("失败时不能标记通过：code=%d writes=%v verified=%d", code, *writes, customer.Verified)
			}
		})
	}
}

func TestCustomerResponsePreservesVerificationStatus(t *testing.T) {
	for _, status := range []uint{1, 2, 3, 4} {
		if actual := toResponse(system.Customer{Verified: status}).Verified; actual != status {
			t.Fatalf("缺少材料不能覆盖后台认证状态：期望%d，实际%d", status, actual)
		}
	}
}
