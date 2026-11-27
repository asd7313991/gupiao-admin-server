package positionsettlement

import (
	"errors"
	"math"
	"testing"
	"time"

	"api-server/db/pgdb/system"
)

func moment(t *testing.T, value string) time.Time {
	t.Helper()
	got, err := time.ParseInLocation(time.DateTime, value, Location)
	if err != nil {
		t.Fatal(err)
	}
	return got
}

func TestNextTradingDay(t *testing.T) {
	for _, test := range []struct{ buy, want string }{
		{"2026-11-19 10:00:00", "2026-11-20"},
		{"2026-11-20 10:00:00", "2026-11-23"},
		{"2026-09-30 15:00:00", "2026-10-08"},
		{"2026-02-13 15:00:00", "2026-02-24"},
		{"2026-05-08 10:00:00", "2026-05-11"},
	} {
		bought := moment(t, test.buy).UTC()
		got, err := NextTradingDay(bought)
		if err != nil || got.Format(time.DateOnly) != test.want || got.Hour() != 0 {
			t.Fatalf("%s: got %v, err %v", test.buy, got, err)
		}
	}
	if _, err := NextTradingDay(moment(t, "2026-12-31 10:00:00")); err == nil {
		t.Fatal("未知年份不能假设所有工作日都开市")
	}
}

func TestBuysSettleWithoutUnlockingSellOrderFreeze(t *testing.T) {
	friday := moment(t, "2026-11-20 10:00:00")
	p := system.TradePosition{Symbol: "600103.SH", SettlementVerified: true, PositionQty: 1000, AvailableQty: 700}
	if err := AddBuy(&p, 200, friday); err != nil {
		t.Fatal(err)
	}
	p.PositionQty += 200
	if err := AddBuy(&p, 100, friday.Add(time.Hour)); err != nil {
		t.Fatal(err)
	}
	p.PositionQty += 100
	for _, at := range []string{"2026-11-20 23:59:59", "2026-11-21 12:00:00", "2026-11-22 23:59:59"} {
		available, message := Preview(p, moment(t, at))
		if available != 700 || message == "" {
			t.Fatalf("旧股仍可卖，新股不能在周末解锁：%v %s", available, message)
		}
	}
	if err := Refresh(&p, moment(t, "2026-11-23 00:00:00")); err != nil {
		t.Fatal(err)
	}
	if p.AvailableQty != 1000 || p.UnsettledQty != 0 {
		t.Fatalf("应只释放300新股，不释放300挂单冻结股：%+v", p)
	}
	if err := Refresh(&p, moment(t, "2026-11-23 10:00:00")); err != nil || p.AvailableQty != 1000 {
		t.Fatal("重复读取不能重复解锁")
	}
}

func TestNewPositionAndHistoricalReview(t *testing.T) {
	now := moment(t, "2026-11-20 10:00:00")
	p := system.TradePosition{Symbol: "600103.SH"}
	if err := AddBuy(&p, 500, now); err != nil {
		t.Fatal(err)
	}
	p.PositionQty = 500
	if available, notice := Preview(p, now); available != 0 || notice == "" {
		t.Fatal("新买入当天应0股可卖且明确提示")
	}
	old := system.TradePosition{Symbol: p.Symbol, PositionQty: 1000, AvailableQty: 1000}
	if err := Refresh(&old, now); !errors.Is(err, ErrReview) {
		t.Fatal("历史缺少批次的持仓须先核对")
	}
	if available, notice := Preview(old, now); available != 0 || notice == "" {
		t.Fatal("历史核对前不能展示旧可卖数量")
	}
	if err := Confirm(&old, 200, now.Unix(), now); err != nil || old.AvailableQty != 800 || old.UnsettledQty != 200 {
		t.Fatalf("后台补录应区分已交收和待交收：%+v %v", old, err)
	}
	for _, pending := range []float64{-1, 1001, math.NaN(), math.Inf(1)} {
		if err := Confirm(&old, pending, now.Unix(), now); err == nil {
			t.Fatal("不能接受异常待交收数量")
		}
	}
	if err := Confirm(&old, 200, now.Add(time.Hour).Unix(), now); err == nil {
		t.Fatal("不能接受未来买入时间")
	}
	if err := Confirm(&old, 1000, moment(t, "2025-12-01 10:00:00").Unix(), now); err != nil || old.AvailableQty != 1000 || old.UnsettledQty != 0 {
		t.Fatal("已核准的历史买入已跨过已知开市日，应可全部卖出", err)
	}
}

func TestReconcileLegacyPositionFromCompleteTradeBatches(t *testing.T) {
	firstBuy := moment(t, "2026-11-20 10:00:00")
	mondayBuy := moment(t, "2026-11-23 10:00:00")
	trades := []system.TradeRecord{
		{Symbol: "600103.SH", Direction: "买入", Quantity: 400, TradeAt: firstBuy.Unix()},
		{Symbol: "600103.SH", Direction: "买入", Quantity: 300, TradeAt: mondayBuy.Unix()},
		{Symbol: "600103.SH", Direction: "卖出", Quantity: 200, TradeAt: moment(t, "2026-11-23 11:00:00").Unix()},
		{Symbol: "600103.SH", Direction: "买入", Quantity: 300, TradeAt: moment(t, "2026-11-24 10:00:00").Unix()},
	}
	position := system.TradePosition{Symbol: "600103.SH", PositionQty: 800, AvailableQty: 800, BuyAt: firstBuy.Unix()}
	if err := ReconcileLegacy(&position, trades, moment(t, "2026-11-24 10:30:00")); err != nil {
		t.Fatal(err)
	}
	if !position.SettlementVerified || position.AvailableQty != 500 || position.UnsettledQty != 300 ||
		position.SellableAt != moment(t, "2026-11-25 00:00:00").Unix() {
		t.Fatalf("应按成交批次解锁旧股，只冻结当日买入：%+v", position)
	}
	if err := Refresh(&position, moment(t, "2026-11-25 00:00:00")); err != nil ||
		position.AvailableQty != 800 || position.UnsettledQty != 0 {
		t.Fatalf("下一交易日应解锁剩余批次：%+v, %v", position, err)
	}

	incomplete := system.TradePosition{Symbol: "600103.SH", PositionQty: 900, AvailableQty: 900, BuyAt: firstBuy.Unix()}
	if err := ReconcileLegacy(&incomplete, trades, moment(t, "2026-11-24 10:30:00")); !errors.Is(err, ErrReview) ||
		incomplete.SettlementVerified || incomplete.AvailableQty != 900 {
		t.Fatalf("成交记录与当前持仓不符时必须保留核对保护：%+v, %v", incomplete, err)
	}
}

func TestReconcileLegacyAcceptsSameDayTimestampDriftAndUsesVerifiedFrozenQuantity(t *testing.T) {
	buyAt := moment(t, "2026-11-20 13:55:12")
	trades := []system.TradeRecord{
		{Symbol: "600410.SH", Direction: "买入", Quantity: 35900, TradeAt: buyAt.Unix()},
		{Symbol: "600410.SH", Direction: "卖出", Quantity: 100, TradeAt: moment(t, "2026-11-20 14:07:42").Unix()},
	}
	position := system.TradePosition{
		Symbol: "600410.SH", PositionQty: 35800, AvailableQty: 0,
		BuyAt: moment(t, "2026-11-20 13:55:11").Unix(),
	}
	if err := ReconcileLegacyWithFrozen(&position, trades, moment(t, "2026-11-27 10:00:00"), 0); err != nil {
		t.Fatal("同日内的持仓与成交时间差异不应阻止完整批次自动核对", err)
	}
	if !position.SettlementVerified || position.AvailableQty != 35800 || position.UnsettledQty != 0 {
		t.Fatalf("全部已交收股数应恢复为可卖：%+v", position)
	}

	frozen := system.TradePosition{
		Symbol: "600410.SH", PositionQty: 35800, AvailableQty: 0,
		BuyAt: moment(t, "2026-11-20 13:55:11").Unix(),
	}
	if err := ReconcileLegacyWithFrozen(&frozen, trades, moment(t, "2026-11-27 10:00:00"), 100); err != nil {
		t.Fatal(err)
	}
	if frozen.AvailableQty != 35700 {
		t.Fatalf("应保留已核实的挂单冻结股数：%+v", frozen)
	}
}

func TestReconcileLegacyFromBuyAtUsesTPlusOneAndReservesPendingSells(t *testing.T) {
	boughtAt := moment(t, "2026-11-20 13:55:11")
	position := system.TradePosition{Symbol: "600410.SH", PositionQty: 1000, BuyAt: boughtAt.Unix()}
	if err := ReconcileLegacyFromBuyAt(&position, moment(t, "2026-11-20 15:00:00"), 100); err != nil {
		t.Fatal(err)
	}
	if !position.SettlementVerified || position.AvailableQty != 0 || position.UnsettledQty != 900 ||
		position.SellableAt != moment(t, "2026-11-23 00:00:00").Unix() {
		t.Fatalf("未过T+1的股数仍不可卖，且应保留待成交冻结：%+v", position)
	}
	if err := Refresh(&position, moment(t, "2026-11-23 00:00:00")); err != nil ||
		position.AvailableQty != 900 || position.UnsettledQty != 0 {
		t.Fatalf("T+1后只释放未被冻结股数：%+v, %v", position, err)
	}

	incomplete := system.TradePosition{Symbol: "600410.SH", PositionQty: 1000, BuyAt: boughtAt.Unix()}
	if err := ReconcileLegacyFromBuyAt(&incomplete, moment(t, "2026-11-23 10:00:00"), 100); err != nil ||
		incomplete.AvailableQty != 900 || incomplete.UnsettledQty != 0 {
		t.Fatalf("历史记录不完整时应依据买入时间判T+1并扣除冻结：%+v, %v", incomplete, err)
	}
}

func TestHongKongStaysTPlusZero(t *testing.T) {
	now := moment(t, "2026-11-20 10:00:00")
	p := system.TradePosition{Symbol: "00700.HK", Currency: "HKD", PositionQty: 100, AvailableQty: 50}
	if err := AddBuy(&p, 200, now); err != nil {
		t.Fatal(err)
	}
	p.PositionQty += 200
	if available, notice := Preview(p, now); available != 250 || notice != "" {
		t.Fatal("港股当天新增200股可卖，原挂单冻结50股不变")
	}
}
