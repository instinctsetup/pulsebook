// Package pb is a limit-order-book matching engine with strict price-time
// priority: the same model Binance, NYSE and every central limit order book
// use. Prices are fixed-point int64 ticks (no floating point anywhere near
// money). A bid crosses when its price >= the best ask; the resting order's
// price sets the trade price; within a level, earlier orders fill first.
package pb

import (
	"container/list"
	"errors"
	"fmt"
	"sort"
	"sync"
	"time"
)

type Side int8

const (
	Buy Side = iota + 1
	Sell
)

func (s Side) String() string {
	if s == Buy {
		return "buy"
	}
	return "sell"
}

var (
	ErrNoOrder   = errors.New("order not found")
	ErrBadQty    = errors.New("quantity must be > 0")
	ErrBadPrice  = errors.New("limit price must be > 0")
	ErrCrossSelf = errors.New("order would cross itself")
)

// Order is one resting or incoming order. Price is in ticks (e.g. 0.01 USDT
// for BTCUSDT); Qty is in base units (e.g. 1e-6 BTC).
type Order struct {
	ID    uint64
	Side  Side
	Price int64
	Qty   int64 // remaining
	Time  int64 // sequence number (arrival order)
	el    *list.Element
}

// Trade is one execution. Price is always the resting (maker) order's price.
type Trade struct {
	Price     int64
	Qty       int64
	MakerID   uint64
	TakerID   uint64
	TakerSide Side
	Seq       int64
}

// level is one price's FIFO queue.
type level struct {
	price  int64
	orders *list.List // of *Order, front = oldest
	total  int64
}

// Book is one instrument's central limit order book.
type Book struct {
	mu     sync.Mutex
	bids   map[int64]*level
	asks   map[int64]*level
	bidPx  []int64 // sorted descending
	askPx  []int64 // sorted ascending
	orders map[uint64]*Order
	seq    int64
	trades []Trade

	// stats
	OrdersAdded   int64
	OrdersCanc    int64
	Trades        int64
	VolumeMatched int64
}

func NewBook() *Book {
	return &Book{
		bids:   map[int64]*level{},
		asks:   map[int64]*level{},
		orders: map[uint64]*Order{},
	}
}

func (b *Book) nextSeq() int64 { b.seq++; return b.seq }

// AddLimit inserts a limit order, matching against the opposite side first.
// Returns the trades executed (possibly none) and the resting remainder.
func (b *Book) AddLimit(id uint64, side Side, price, qty int64) ([]Trade, error) {
	if qty <= 0 {
		return nil, ErrBadQty
	}
	if price <= 0 {
		return nil, ErrBadPrice
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	o := &Order{ID: id, Side: side, Price: price, Qty: qty, Time: b.nextSeq()}
	trades := b.matchLocked(o, price)
	if o.Qty > 0 {
		b.restLocked(o)
	}
	b.OrdersAdded++
	return trades, nil
}

// AddMarket matches immediately at whatever prices are available; the
// unfilled remainder is cancelled (IOC semantics).
func (b *Book) AddMarket(id uint64, side Side, qty int64) ([]Trade, error) {
	if qty <= 0 {
		return nil, ErrBadQty
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	limit := int64(1) << 62 // effectively infinite
	if side == Sell {
		limit = 0
	}
	o := &Order{ID: id, Side: side, Price: limit, Qty: qty, Time: b.nextSeq()}
	trades := b.matchLocked(o, limit)
	b.OrdersAdded++
	return trades, nil
}

// matchLocked executes an incoming order against the opposite side while its
// limit crosses. Returns trades; mutates o.Qty.
func (b *Book) matchLocked(o *Order, limit int64) []Trade {
	var trades []Trade
	for o.Qty > 0 {
		var lv *level
		if o.Side == Buy {
			lv = b.bestAskLocked()
			if lv == nil || lv.price > limit {
				break
			}
		} else {
			lv = b.bestBidLocked()
			if lv == nil || lv.price < limit {
				break
			}
		}
		el := lv.orders.Front()
		maker := el.Value.(*Order)
		q := o.Qty
		if maker.Qty < q {
			q = maker.Qty
		}
		o.Qty -= q
		maker.Qty -= q
		lv.total -= q
		t := Trade{
			Price: maker.Price, Qty: q,
			MakerID: maker.ID, TakerID: o.ID, TakerSide: o.Side,
			Seq: b.nextSeq(),
		}
		trades = append(trades, t)
		b.trades = append(b.trades, t)
		b.Trades++
		b.VolumeMatched += q
		if maker.Qty == 0 {
			lv.orders.Remove(el)
			delete(b.orders, maker.ID)
		}
		if lv.orders.Len() == 0 {
			b.dropLevelLocked(o.Side, lv.price)
		}
	}
	return trades
}

func (b *Book) restLocked(o *Order) {
	var lv *level
	if o.Side == Buy {
		lv = b.bids[o.Price]
		if lv == nil {
			lv = &level{price: o.Price, orders: list.New()}
			b.bids[o.Price] = lv
			b.bidPx = insertSortedDesc(b.bidPx, o.Price)
		}
	} else {
		lv = b.asks[o.Price]
		if lv == nil {
			lv = &level{price: o.Price, orders: list.New()}
			b.asks[o.Price] = lv
			b.askPx = insertSortedAsc(b.askPx, o.Price)
		}
	}
	o.el = lv.orders.PushBack(o) // FIFO: arrival order within the level
	lv.total += o.Qty
	b.orders[o.ID] = o
}

func (b *Book) dropLevelLocked(incoming Side, price int64) {
	if incoming == Buy {
		delete(b.asks, price)
		b.askPx = removeSorted(b.askPx, price)
	} else {
		delete(b.bids, price)
		b.bidPx = removeSorted(b.bidPx, price)
	}
}

func (b *Book) bestBidLocked() *level {
	if len(b.bidPx) == 0 {
		return nil
	}
	return b.bids[b.bidPx[0]]
}

func (b *Book) bestAskLocked() *level {
	if len(b.askPx) == 0 {
		return nil
	}
	return b.asks[b.askPx[0]]
}

// Cancel removes a resting order.
func (b *Book) Cancel(id uint64) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	o, ok := b.orders[id]
	if !ok {
		return ErrNoOrder
	}
	var lv *level
	if o.Side == Buy {
		lv = b.bids[o.Price]
	} else {
		lv = b.asks[o.Price]
	}
	lv.orders.Remove(o.el)
	lv.total -= o.Qty
	if lv.orders.Len() == 0 {
		if o.Side == Buy {
			delete(b.bids, o.Price)
			b.bidPx = removeSorted(b.bidPx, o.Price)
		} else {
			delete(b.asks, o.Price)
			b.askPx = removeSorted(b.askPx, o.Price)
		}
	}
	delete(b.orders, id)
	b.OrdersCanc++
	return nil
}

// Level is the public view of one price level.
type Level struct {
	Price     int64 `json:"price"`
	Qty       int64 `json:"qty"`
	NumOrders int   `json:"orders"`
}

// Depth returns up to n levels per side, best first.
func (b *Book) Depth(n int) (bids, asks []Level) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for i := 0; i < len(b.bidPx) && i < n; i++ {
		lv := b.bids[b.bidPx[i]]
		bids = append(bids, Level{lv.price, lv.total, lv.orders.Len()})
	}
	for i := 0; i < len(b.askPx) && i < n; i++ {
		lv := b.asks[b.askPx[i]]
		asks = append(asks, Level{lv.price, lv.total, lv.orders.Len()})
	}
	return bids, asks
}

// RecentTrades returns the last n trades, newest first.
func (b *Book) RecentTrades(n int) []Trade {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []Trade
	for i := len(b.trades) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, b.trades[i])
	}
	return out
}

// Stats counters.
func (b *Book) Stats() map[string]int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return map[string]int64{
		"orders_added":   b.OrdersAdded,
		"orders_canc":    b.OrdersCanc,
		"trades":         b.Trades,
		"volume_matched": b.VolumeMatched,
		"resting":        int64(len(b.orders)),
		"bid_levels":     int64(len(b.bidPx)),
		"ask_levels":     int64(len(b.askPx)),
	}
}

// CheckInvariants returns an error if price-time priority or book integrity is
// violated. Used by tests and by the replay harness after every batch.
func (b *Book) CheckInvariants() error {
	b.mu.Lock()
	defer b.mu.Unlock()
	if len(b.bidPx) > 0 && len(b.askPx) > 0 && b.bidPx[0] >= b.askPx[0] {
		return fmt.Errorf("crossed book: bid %d >= ask %d", b.bidPx[0], b.askPx[0])
	}
	if !sort.SliceIsSorted(b.bidPx, func(i, j int) bool { return b.bidPx[i] > b.bidPx[j] }) {
		return errors.New("bids not sorted descending")
	}
	if !sort.SliceIsSorted(b.askPx, func(i, j int) bool { return b.askPx[i] < b.askPx[j] }) {
		return errors.New("asks not sorted ascending")
	}
	for _, px := range b.bidPx {
		lv := b.bids[px]
		var prev int64 = -1
		for el := lv.orders.Front(); el != nil; el = el.Next() {
			o := el.Value.(*Order)
			if o.Qty <= 0 {
				return fmt.Errorf("zero-qty order %d resting", o.ID)
			}
			if prev >= 0 && o.Time < prev {
				return fmt.Errorf("time priority violated at bid %d", px)
			}
			prev = o.Time
		}
	}
	for _, px := range b.askPx {
		lv := b.asks[px]
		var prev int64 = -1
		for el := lv.orders.Front(); el != nil; el = el.Next() {
			o := el.Value.(*Order)
			if o.Qty <= 0 {
				return fmt.Errorf("zero-qty order %d resting", o.ID)
			}
			if prev >= 0 && o.Time < prev {
				return fmt.Errorf("time priority violated at ask %d", px)
			}
			prev = o.Time
		}
	}
	return nil
}

// TimeNow is a helper for callers that want wall-clock tagging.
func TimeNow() int64 { return time.Now().UnixNano() }

func insertSortedAsc(s []int64, v int64) []int64 {
	i := sort.Search(len(s), func(i int) bool { return s[i] >= v })
	s = append(s, 0)
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

func insertSortedDesc(s []int64, v int64) []int64 {
	i := sort.Search(len(s), func(i int) bool { return s[i] <= v })
	s = append(s, 0)
	copy(s[i+1:], s[i:])
	s[i] = v
	return s
}

func removeSorted(s []int64, v int64) []int64 {
	for j, x := range s {
		if x == v {
			return append(s[:j], s[j+1:]...)
		}
	}
	return s
}
