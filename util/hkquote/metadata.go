package hkquote

import (
	"fmt"
	"math"
	"strconv"
	"time"
)

type Metadata struct {
	LotSize  float64
	Currency string
	Kind     string
	At       int64
}

func ParseTencentMetadata(fields []string) (Metadata, error) {
	if len(fields) <= 75 {
		return Metadata{}, fmt.Errorf("港股证券资料字段不完整")
	}
	lot, err := strconv.ParseFloat(fields[60], 64)
	if err != nil || math.IsNaN(lot) || math.IsInf(lot, 0) || lot < 1 || lot > 1_000_000 || lot != math.Trunc(lot) {
		return Metadata{}, fmt.Errorf("港股每手股数无效")
	}
	at, err := time.ParseInLocation("2006/01/02 15:04:05", fields[30], time.FixedZone("HK", 8*3600))
	if err != nil || fields[75] == "" || fields[63] == "" {
		return Metadata{}, fmt.Errorf("港股证券资料币种、品种或源端时间无效")
	}
	return Metadata{LotSize: lot, Currency: fields[75], Kind: fields[63], At: at.Unix()}, nil
}

func (m Metadata) ValidAt(now time.Time) bool {
	return m.At > 0 && m.At <= now.Unix()+60 && now.Unix()-m.At <= 24*60*60 &&
		m.Currency != "" && m.Kind != "" && m.LotSize >= 1 && m.LotSize <= 1_000_000 &&
		!math.IsNaN(m.LotSize) && !math.IsInf(m.LotSize, 0) && m.LotSize == math.Trunc(m.LotSize)
}
