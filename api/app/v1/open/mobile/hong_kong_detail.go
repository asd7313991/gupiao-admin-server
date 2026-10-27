package mobile

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"api-server/db/pgdb/system"
)

func hongKongDetailNumber(text string, positive bool) (float64, error) {
	value, err := strconv.ParseFloat(text, 64)
	if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || positive && value == 0 {
		return 0, fmt.Errorf("港股行情数值无效：%q", text)
	}
	return value, nil
}

func parseHongKongOrderBook(body string, item system.StockSecurity, now time.Time) (orderBookView, error) {
	snapshot, fields, err := parseHongKongDetailSnapshot(body, item, now)
	if err != nil {
		return orderBookView{}, err
	}
	book := orderBookView{Code: item.Code, Name: item.Name, LastPrice: snapshot.LastPrice,
		PrevClose: snapshot.PrevClose, Open: snapshot.Open, Change: snapshot.LastPrice - snapshot.PrevClose,
		ChangeRate: snapshot.ChangeRate, UpdatedAt: time.Unix(snapshot.QuoteAt, 0).In(time.FixedZone("UTC+8", 8*3600)).Format(time.RFC3339),
		Source: snapshot.Source, QuoteAt: snapshot.QuoteAt, Bid: []orderBookLevel{}, Ask: []orderBookLevel{}}
	for _, side := range []struct {
		start int
		dest  *[]orderBookLevel
	}{{9, &book.Bid}, {19, &book.Ask}} {
		for i := 0; i < 5; i++ {
			index := side.start + i*2
			price, err := hongKongDetailNumber(fields[index], false)
			if err != nil {
				return orderBookView{}, err
			}
			volume, err := hongKongDetailNumber(fields[index+1], false)
			if err != nil || price == 0 && volume > 0 {
				return orderBookView{}, fmt.Errorf("港股盘口档位无效")
			}
			if price == 0 {
				continue
			}
			// 公开源的零数量表示未提供，不据此声称有零股挂单。
			available := volume > 0
			*side.dest = append(*side.dest, orderBookLevel{Level: i + 1, Price: price, Volume: volume, VolumeAvailable: &available})
		}
	}
	return book, nil
}

func parseHongKongMinuteDays(days []tencentMinuteDay, fiveDays bool) ([]kLinePoint, error) {
	points := make([]kLinePoint, 0, len(days)*330)
	seen := make(map[string]bool)
	for _, day := range days {
		date, err := time.Parse("20060102", day.Date)
		if err != nil {
			return nil, fmt.Errorf("港股分时日期无效：%q", day.Date)
		}
		var previousClose float64
		if fiveDays {
			previousClose, err = hongKongDetailNumber(day.Prec, true)
			if err != nil {
				return nil, fmt.Errorf("港股五日图缺少当日昨收：%w", err)
			}
		}
		var previousVolume, previousAmount float64
		lastTime := ""
		for _, line := range day.Data {
			fields := strings.Fields(line)
			if len(fields) < 4 || len(fields[0]) != 4 {
				return nil, fmt.Errorf("港股分时字段缺失")
			}
			if _, err := time.Parse("1504", fields[0]); err != nil || fields[0] <= lastTime {
				return nil, fmt.Errorf("港股分时时间无效或未递增")
			}
			lastTime = fields[0]
			price, err := hongKongDetailNumber(fields[1], true)
			if err != nil {
				return nil, err
			}
			volume, err := hongKongDetailNumber(fields[2], false)
			if err != nil {
				return nil, err
			}
			amount, err := hongKongDetailNumber(fields[3], false)
			if err != nil || volume < previousVolume || amount < previousAmount || volume == 0 && amount > 0 {
				return nil, fmt.Errorf("港股分时累计成交数据无效")
			}
			label := date.Format("2006-01-02") + " " + fields[0][:2] + ":" + fields[0][2:]
			if seen[label] {
				return nil, fmt.Errorf("港股分时点重复：%s", label)
			}
			seen[label] = true
			var average float64
			if volume > 0 {
				average = amount / volume
			}
			points = append(points, kLinePoint{Time: label, Open: price, High: price, Low: price, Close: price,
				Volume: volume - previousVolume, Amount: amount - previousAmount, Turnover: amount - previousAmount,
				AveragePrice: average, PrevClose: previousClose})
			previousVolume, previousAmount = volume, amount
		}
	}
	if len(points) == 0 {
		return nil, fmt.Errorf("港股分时数据为空")
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Time < points[j].Time })
	return points, nil
}

func parseHongKongKLineRows(rows [][]json.RawMessage) ([]kLinePoint, error) {
	points := make([]kLinePoint, 0, len(rows))
	seen := make(map[string]bool)
	for _, row := range rows {
		if len(row) < 6 {
			return nil, fmt.Errorf("港股K线字段缺失")
		}
		date := rawString(row[0])
		if _, err := time.Parse("2006-01-02", date); err != nil || seen[date] {
			return nil, fmt.Errorf("港股K线日期无效或重复")
		}
		seen[date] = true
		values := make([]float64, 5)
		for i := range values {
			value, err := hongKongDetailNumber(rawString(row[i+1]), i < 4)
			if err != nil {
				return nil, err
			}
			values[i] = value
		}
		open, closePrice, high, low := values[0], values[1], values[2], values[3]
		if low > high || open < low || open > high || closePrice < low || closePrice > high {
			return nil, fmt.Errorf("港股K线高低价范围无效")
		}
		// 港股日周月源端第七列可能是公司行动资料，并非成交额。
		available := false
		points = append(points, kLinePoint{Time: date, Open: open, Close: closePrice, High: high, Low: low,
			Volume: values[4], AmountAvailable: &available})
	}
	if len(points) == 0 {
		return nil, fmt.Errorf("港股K线数据为空")
	}
	sort.Slice(points, func(i, j int) bool { return points[i].Time < points[j].Time })
	return points, nil
}
