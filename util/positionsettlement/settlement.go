package positionsettlement

import (
	"errors"
	"fmt"
	"math"
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
