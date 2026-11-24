package positionsettlement

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"api-server/db/pgdb/system"
)

var Location = time.FixedZone("Asia/Shanghai", 8*60*60)
var ErrReview = errors.New("A股历史持仓买入批次待后台核对，暂不可卖出或加仓")

// 上交所2026年休市安排：https://www.sse.com.cn/disclosure/dealinstruc/closed/c/c_20251222_10802510.shtml
var holidays = [][2]string{
	{"2026-01-01", "2026-01-03"}, {"2026-02-15", "2026-02-23"},
	{"2026-04-04", "2026-04-06"}, {"2026-05-01", "2026-05-05"},
	{"2026-06-19", "2026-06-21"}, {"2026-09-25", "2026-09-27"},
	{"2026-10-01", "2026-10-07"},
}

func HongKong(p system.TradePosition) bool {
	return strings.HasSuffix(p.Symbol, ".HK")
}

func NextTradingDay(bought time.Time) (time.Time, error) {
	bought = bought.In(Location)
	day := time.Date(bought.Year(), bought.Month(), bought.Day()+1, 0, 0, 0, 0, Location)
	for {
		if day.Year() != 2026 {
			return time.Time{}, fmt.Errorf("A股%d年交易日历尚未维护，请联系管理员，不能确定可卖日期", day.Year())
		}
		closed := day.Weekday() == time.Saturday || day.Weekday() == time.Sunday
		for _, period := range holidays {
			date := day.Format(time.DateOnly)
			closed = closed || (date >= period[0] && date <= period[1])
		}
		if !closed {
			return day, nil
		}
		day = day.AddDate(0, 0, 1)
	}
}

func validQuantity(q float64) bool {
	return !math.IsNaN(q) && !math.IsInf(q, 0) && q >= 0
}

func validShareQuantity(q float64) bool {
	return validQuantity(q) && q == math.Trunc(q)
}

// Refresh 在持仓行锁内调用并随交易保存；只读展示应在副本上调用。
func Refresh(p *system.TradePosition, now time.Time) error {
	if HongKong(*p) {
		return nil
	}
	if !p.SettlementVerified {
		return ErrReview
	}
	if !validQuantity(p.UnsettledQty) || !validQuantity(p.AvailableQty) ||
		p.AvailableQty+p.UnsettledQty > p.PositionQty || (p.UnsettledQty > 0 && p.SellableAt <= 0) {
		return errors.New("A股交收数量异常，请后台核对后交易")
	}
	if p.UnsettledQty > 0 && now.Unix() >= p.SellableAt {
		p.AvailableQty += p.UnsettledQty
		p.UnsettledQty, p.SellableAt = 0, 0
	}
	return nil
}

// ReconcileLegacy reconstructs an unverified position only when its complete trade
// history matches the current quantity. Incomplete histories remain locked for review.
func ReconcileLegacy(p *system.TradePosition, records []system.TradeRecord, now time.Time) error {
	if HongKong(*p) || p.SettlementVerified {
		return Refresh(p, now)
	}
	if !validShareQuantity(p.PositionQty) || p.PositionQty <= 0 ||
		!validShareQuantity(p.AvailableQty) || p.AvailableQty > p.PositionQty || len(records) == 0 {
		return ErrReview
	}

	ordered := append([]system.TradeRecord(nil), records...)
	sort.SliceStable(ordered, func(i, j int) bool {
		if ordered[i].TradeAt == ordered[j].TradeAt {
			return ordered[i].ID < ordered[j].ID
		}
		return ordered[i].TradeAt < ordered[j].TradeAt
	})
	type lot struct {
		remaining float64
		boughtAt  int64
	}
	lots := make([]lot, 0, len(ordered))
	firstBuyAt := int64(0)
	for _, record := range ordered {
		if record.Symbol != p.Symbol || !validShareQuantity(record.Quantity) || record.Quantity <= 0 ||
			record.TradeAt <= 0 || record.TradeAt > now.Unix() {
			return ErrReview
		}
		switch record.Direction {
		case "买入":
			if firstBuyAt == 0 {
				firstBuyAt = record.TradeAt
			}
			lots = append(lots, lot{remaining: record.Quantity, boughtAt: record.TradeAt})
		case "卖出":
			remaining := record.Quantity
			for index := range lots {
				if remaining == 0 {
					break
				}
				if lots[index].boughtAt > record.TradeAt {
					return ErrReview
				}
				used := math.Min(lots[index].remaining, remaining)
				lots[index].remaining -= used
				remaining -= used
			}
			if remaining > 0 {
				return ErrReview
			}
		default:
			return ErrReview
		}
	}
	if firstBuyAt == 0 || (p.BuyAt > 0 && p.BuyAt != firstBuyAt) {
		return ErrReview
	}

	var reconstructed, settled, unsettled float64
	var sellableAt int64
	for _, item := range lots {
		if item.remaining == 0 {
			continue
		}
		reconstructed += item.remaining
		firstKnownOpen := time.Date(2026, 1, 5, 0, 0, 0, 0, Location)
		var unlock int64
		if time.Unix(item.boughtAt, 0).In(Location).Year() < 2026 && !now.Before(firstKnownOpen) {
			unlock = firstKnownOpen.Unix()
		} else {
			day, err := NextTradingDay(time.Unix(item.boughtAt, 0))
			if err != nil {
				return ErrReview
			}
			unlock = day.Unix()
		}
		if now.Unix() >= unlock {
			settled += item.remaining
		} else {
			if sellableAt != 0 && sellableAt != unlock {
				return ErrReview
			}
			sellableAt = unlock
			unsettled += item.remaining
		}
	}
	if math.Abs(reconstructed-p.PositionQty) > 0.000001 {
		return ErrReview
	}
	frozen := p.PositionQty - p.AvailableQty
	if settled < frozen {
		return ErrReview
	}
	p.SettlementVerified = true
	p.AvailableQty = settled - frozen
	p.UnsettledQty = unsettled
	p.SellableAt = sellableAt
	return Refresh(p, now)
}

func Preview(p system.TradePosition, now time.Time) (float64, string) {
	if err := Refresh(&p, now); err != nil {
		return 0, err.Error()
	}
	if !HongKong(p) && p.UnsettledQty > 0 {
		return p.AvailableQty, fmt.Sprintf("A股待交收 %.0f 股，%s起可卖（北京时间，仍须符合交易时段）",
			p.UnsettledQty, time.Unix(p.SellableAt, 0).In(Location).Format(time.DateOnly))
	}
	return p.AvailableQty, ""
}

// AddBuy 在已刷新交收状态、尚未增加总持仓时调用。
func AddBuy(p *system.TradePosition, quantity float64, bought time.Time) error {
	if !validQuantity(quantity) || quantity == 0 {
		return errors.New("买入股数无效")
	}
	if HongKong(*p) {
		p.AvailableQty += quantity
		return nil
	}
	if p.PositionQty == 0 {
		p.SettlementVerified = true
		p.AvailableQty, p.UnsettledQty, p.SellableAt = 0, 0, 0
	}
	if err := Refresh(p, bought); err != nil {
		return err
	}
	day, err := NextTradingDay(bought)
	if err != nil {
		return err
	}
	if p.UnsettledQty > 0 && p.SellableAt != day.Unix() {
		return errors.New("A股待交收日期不一致，请核对交易日历")
	}
	p.UnsettledQty += quantity
	p.SellableAt = day.Unix()
	return nil
}

// Confirm 仅用于后台已核对的股数拆分；调用者必须先确认无待成交卖单并保存审计快照。
func Confirm(p *system.TradePosition, pending float64, boughtAt int64, now time.Time) error {
	if !validQuantity(p.PositionQty) || !validQuantity(pending) || pending > p.PositionQty {
		return errors.New("待交收股数必须在0和持仓数量之间")
	}
	p.SettlementVerified = true
	p.UnsettledQty, p.SellableAt = pending, 0
	p.AvailableQty = p.PositionQty - pending
	if pending > 0 {
		if boughtAt <= 0 || boughtAt > now.Unix() {
			return errors.New("请填写有效的待交收买入时间，不能晚于当前时间")
		}
		// 更早的历史买入无需猜测当年的假期；已跨过一个已知开市日即可证明已交收。
		firstKnownOpen := time.Date(2026, 1, 5, 0, 0, 0, 0, Location)
		if time.Unix(boughtAt, 0).In(Location).Year() < 2026 && !now.Before(firstKnownOpen) {
			p.AvailableQty += p.UnsettledQty
			p.UnsettledQty = 0
			return Refresh(p, now)
		}
		day, err := NextTradingDay(time.Unix(boughtAt, 0))
		if err != nil {
			return err
		}
		p.SellableAt = day.Unix()
	}
	return Refresh(p, now)
}
