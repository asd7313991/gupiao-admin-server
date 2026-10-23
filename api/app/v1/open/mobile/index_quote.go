package mobile

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"

	"api-server/api/response"
)

type marketIndexView struct {
	Code       string  `json:"code"`
	Symbol     string  `json:"symbol"`
	Name       string  `json:"name"`
	Value      float64 `json:"value"`
	Change     float64 `json:"change"`
	ChangeRate float64 `json:"change_rate"`
	Volume     float64 `json:"volume"`
	Amount     float64 `json:"amount"`
	QuoteAt    int64   `json:"quote_at"`
}

var mobileIndexSymbols = []string{"sh000001", "sz399001", "sz399006", "sh000300", "sh000905"}

func ListMarketIndices(c *gin.Context) {
	c.Header("Cache-Control", "no-store")
	request, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, "https://qt.gtimg.cn/q="+strings.Join(mobileIndexSymbols, ","), nil)
	if err != nil {
		response.ReturnError(c, response.INTERNAL, "指数行情请求失败")
		return
	}
	request.Header.Set("User-Agent", "Mozilla/5.0")
	result, err := (&http.Client{Timeout: 8 * time.Second}).Do(request)
	if err != nil {
		response.ReturnError(c, response.UNAVAILABLE, "指数行情暂时不可用")
		return
	}
	defer result.Body.Close()
	if result.StatusCode != http.StatusOK {
		response.ReturnError(c, response.UNAVAILABLE, "指数行情暂时不可用")
		return
	}
	content, err := io.ReadAll(transform.NewReader(result.Body, simplifiedchinese.GBK.NewDecoder()))
	if err != nil {
		response.ReturnError(c, response.DATA_LOSS, "指数行情解析失败")
		return
	}
	indices := parseTencentIndices(string(content))
	if len(indices) != len(mobileIndexSymbols) {
		response.ReturnError(c, response.UNAVAILABLE, "指数行情暂无数据")
		return
	}
	response.ReturnData(c, indices)
}

func parseTencentIndices(content string) []marketIndexView {
	rows := strings.Split(content, ";")
	indices := make([]marketIndexView, 0, len(mobileIndexSymbols))
	for _, row := range rows {
		row = strings.TrimSpace(row)
		if row == "" {
			continue
		}
		parts := strings.SplitN(row, "=", 2)
		if len(parts) != 2 {
			continue
		}
		symbol := strings.TrimPrefix(strings.TrimSpace(parts[0]), "v_")
		fields := strings.Split(strings.Trim(parts[1], "\"\r\n "), "~")
		known := false
		for _, expected := range mobileIndexSymbols {
			if symbol == expected {
				known = true
				break
			}
		}
		if !known || len(fields) < 38 || fields[2] != symbol[2:] {
			continue
		}
		values := make([]float64, 5)
		valid := true
		for index, field := range []int{3, 31, 32, 6, 37} {
			value, err := strconv.ParseFloat(strings.TrimSpace(fields[field]), 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) {
				valid = false
				break
			}
			values[index] = value
		}
		quoteTime, err := time.ParseInLocation("20060102150405", fields[30], time.FixedZone("Asia/Shanghai", 8*60*60))
		if !valid || values[0] <= 0 || err != nil {
			continue
		}
		duplicate := false
		for _, item := range indices {
			if item.Symbol == symbol {
				duplicate = true
				break
			}
		}
		if duplicate {
			continue
		}
		indices = append(indices, marketIndexView{
			Code: fields[2], Symbol: symbol, Name: fields[1], Value: values[0],
			Change: values[1], ChangeRate: values[2],
			Volume: values[3], Amount: values[4], QuoteAt: quoteTime.Unix(),
		})
	}
	return indices
}

func (item marketIndexView) String() string {
	return fmt.Sprintf("%s %s %.2f", item.Code, item.Name, item.Value)
}
