package pb

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

// BinanceAggTrade is one row of the public Binance aggTrades dump
// (data.binance.vision): aggId,price,qty,firstId,lastId,timestamp,isBuyerMaker,...
// isBuyerMaker=true means the aggressor was a SELL (maker bought).
type BinanceAggTrade struct {
	Price        float64
	Qty          float64
	TimestampUs  int64
	IsBuyerMaker bool
}

// LoadBinanceAggTrades streams a Binance aggTrades CSV. Up to max rows.
func LoadBinanceAggTrades(path string, max int) ([]BinanceAggTrade, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	var out []BinanceAggTrade
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 1<<20), 1<<20)
	first := true
	for sc.Scan() {
		line := sc.Text()
		if first {
			first = false
			if strings.HasPrefix(line, "agg_trade_id") || strings.HasPrefix(line, "aggTradeId") {
				continue // header
			}
		}
		parts := strings.Split(line, ",")
		if len(parts) < 7 {
			continue
		}
		px, err1 := strconv.ParseFloat(parts[1], 64)
		q, err2 := strconv.ParseFloat(parts[2], 64)
		ts, err3 := strconv.ParseInt(parts[5], 10, 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		out = append(out, BinanceAggTrade{
			Price: px, Qty: q, TimestampUs: ts,
			IsBuyerMaker: parts[6] == "True" || parts[6] == "true",
		})
		if max > 0 && len(out) >= max {
			break
		}
	}
	return out, sc.Err()
}

// ReplayBinance seeds the book with a synthetic ladder around the first real
// trade price, then replays real aggressor trades as market orders. Between
// aggressors it rests passive limit orders near the touch so the book stays
// alive - the same role a market maker plays. Returns trades executed.
//
// tick: price tick (0.01 for BTCUSDT). qtyScale: BTC units per lot (1e-6).
func ReplayBinance(b *Book, rows []BinanceAggTrade, tick float64, qtyScale float64, nextID *uint64) (int, error) {
	if len(rows) == 0 {
		return 0, fmt.Errorf("no rows")
	}
	mid0 := int64(rows[0].Price / tick)
	// seed: 50 levels deep on both sides, ~0.05 BTC per level
	for i := int64(1); i <= 50; i++ {
		if _, err := b.AddLimit(*nextID, Buy, mid0-i, int64(0.05/qtyScale)); err != nil {
			return 0, err
		}
		*nextID++
		if _, err := b.AddLimit(*nextID, Sell, mid0+i, int64(0.05/qtyScale)); err != nil {
			return 0, err
		}
		*nextID++
	}
	exec := 0
	for _, r := range rows {
		qty := int64(r.Qty / qtyScale)
		if qty == 0 {
			qty = 1
		}
		side := Buy // aggressor bought
		if r.IsBuyerMaker {
			side = Sell // aggressor sold
		}
		tr, err := b.AddMarket(*nextID, side, qty)
		if err != nil {
			return exec, err
		}
		exec += len(tr)
		*nextID++
		// market-maker refill: keep ~1 BTC of liquidity visible in the top 3
		// levels per side. Refill at the current mid offsets, so liquidity
		// tracks the market instead of stacking on one stale price.
		bids, asks := b.Depth(3)
		// If a side emptied completely, rebuild its ladder around the other
		// side's touch (a real market maker never leaves the book one-sided).
		if len(bids) == 0 && len(asks) > 0 {
			ref := asks[0].Price - 10
			for j := int64(1); j <= 10; j++ {
				b.AddLimit(*nextID, Buy, ref-j, int64(0.05/qtyScale))
				*nextID++
			}
			bids, asks = b.Depth(3)
		}
		if len(asks) == 0 && len(bids) > 0 {
			ref := bids[0].Price + 10
			for j := int64(1); j <= 10; j++ {
				b.AddLimit(*nextID, Sell, ref+j, int64(0.05/qtyScale))
				*nextID++
			}
			bids, asks = b.Depth(3)
		}
		if len(bids) > 0 && len(asks) > 0 {
			mid := (bids[0].Price + asks[0].Price) / 2
			target := int64(1.0 / qtyScale)
			var bq, aq int64
			for _, l := range bids {
				bq += l.Qty
			}
			for _, l := range asks {
				aq += l.Qty
			}
			for i := int64(1); i <= 3; i++ {
				if bq < target {
					q := int64(0.02 / qtyScale)
					b.AddLimit(*nextID, Buy, mid-i*2, q)
					*nextID++
					bq += q
				}
				if aq < target {
					q := int64(0.02 / qtyScale)
					b.AddLimit(*nextID, Sell, mid+i*2, q)
					*nextID++
					aq += q
				}
			}
		}
	}
	return exec, nil
}
