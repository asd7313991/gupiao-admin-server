package mobile

import (
	"fmt"
	"io"
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
}

var mobileIndexSymbols = []string{"s_sh000001", "s_sz399001", "s_sz399006", "s_sh000300", "s_sh000905"}

func ListMarketIndices(c *gin.Context) {
	request, _ := http.NewRequest(http.MethodGet, "https://qt.gtimg.cn/q="+strings.Join(mobileIndexSymbols, ","), nil)
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
	if len(indices) == 0 {
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
		if len(fields) < 8 {
			continue
		}
		indices = append(indices, marketIndexView{
			Code: fields[2], Symbol: symbol, Name: fields[1], Value: indexNumber(fields[3]),
			Change: indexNumber(fields[4]), ChangeRate: indexNumber(fields[5]),
			Volume: indexNumber(fields[6]), Amount: indexNumber(fields[7]),
		})
	}
	return indices
}

func indexNumber(value string) float64 {
	number, err := strconv.ParseFloat(strings.TrimSpace(value), 64)
	if err != nil {
		return 0
	}
	return number
}

func (item marketIndexView) String() string {
	return fmt.Sprintf("%s %s %.2f", item.Code, item.Name, item.Value)
}
