package mobile

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"api-server/api/response"
	"api-server/db/pgdb/system"
	"github.com/gin-gonic/gin"
)

func TestTradeProfitResponsePreservesUnknownAndZero(t *testing.T) {
	zero, loss := 0.0, -123.45
	recorder := httptest.NewRecorder()
	context, _ := gin.CreateTestContext(recorder)
	response.ReturnData(context, gin.H{"records": []system.TradeRecord{
		{Direction: "卖出"},
		{Direction: "买入"},
		{Direction: "卖出", RealizedProfit: &zero},
		{Direction: "卖出", RealizedProfit: &loss},
	}})
	var result struct {
		Data struct {
			Records []map[string]any `json:"records"`
		} `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	rows := result.Data.Records
	if len(rows) != 4 || rows[0]["realized_profit"] != nil || rows[1]["realized_profit"] != nil || rows[2]["realized_profit"] != float64(0) || rows[3]["realized_profit"] != -123.45 {
		t.Fatalf("历史未知、买入、持平和亏损不能混淆：%s", recorder.Body.String())
	}
}
