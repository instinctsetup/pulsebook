package pb

import (
	"testing"
)

func mustAdd(t *testing.T, b *Book, id uint64, side Side, price, qty int64) []Trade {
	t.Helper()
	tr, err := b.AddLimit(id, side, price, qty)
	if err != nil {
		t.Fatalf("add %d: %v", id, err)
	}
	return tr
}

func TestNoMatchWhenNotCrossing(t *testing.T) {
	b := NewBook()
	tr := mustAdd(t, b, 1, Buy, 100_00, 10) // $100.00
	if len(tr) != 0 {
		t.Fatal("empty book should not trade")
	}
	mustAdd(t, b, 2, Sell, 101_00, 5)
	if err := b.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
	bids, asks := b.Depth(5)
	if bids[0].Price != 100_00 || asks[0].Price != 101_00 {
		t.Fatalf("bad top of book: %+v %+v", bids, asks)
	}
}

func TestFullCrossAtMakerPrice(t *testing.T) {
	b := NewBook()
	mustAdd(t, b, 1, Sell, 100_50, 10)
	tr := mustAdd(t, b, 2, Buy, 101_00, 10) // willing to pay 101, gets 100.50
	if len(tr) != 1 {
		t.Fatalf("want 1 trade, got %d", len(tr))
	}
	if tr[0].Price != 100_50 {
		t.Fatalf("trade must print at maker price 100.50, got %d", tr[0].Price)
	}
	if tr[0].MakerID != 1 || tr[0].TakerID != 2 {
		t.Fatalf("bad parties: %+v", tr[0])
	}
	if b.Stats()["resting"] != 0 {
		t.Fatal("both orders should be gone")
	}
}

func TestPartialFillLeavesRemainder(t *testing.T) {
	b := NewBook()
	mustAdd(t, b, 1, Sell, 100_00, 4)
	tr := mustAdd(t, b, 2, Buy, 100_00, 10)
	if len(tr) != 1 || tr[0].Qty != 4 {
		t.Fatalf("want one trade of 4, got %+v", tr)
	}
	bids, _ := b.Depth(5)
	if len(bids) != 1 || bids[0].Qty != 6 {
		t.Fatalf("want resting bid of 6, got %+v", bids)
	}
}

func TestPriceTimePriority(t *testing.T) {
	b := NewBook()
	// two asks at same price: order 1 arrived first, must fill first
	mustAdd(t, b, 1, Sell, 100_00, 5)
	mustAdd(t, b, 2, Sell, 100_00, 5)
	tr := mustAdd(t, b, 3, Buy, 100_00, 7)
	if len(tr) != 2 {
		t.Fatalf("want 2 trades, got %d", len(tr))
	}
	if tr[0].MakerID != 1 || tr[0].Qty != 5 {
		t.Fatalf("first fill must fully take order 1: %+v", tr[0])
	}
	if tr[1].MakerID != 2 || tr[1].Qty != 2 {
		t.Fatalf("second fill must partially take order 2: %+v", tr[1])
	}
	// order 2 remainder still resting, FIFO position preserved
	_, asks := b.Depth(5)
	if asks[0].Qty != 3 || asks[0].NumOrders != 1 {
		t.Fatalf("bad level after partial: %+v", asks[0])
	}
	if err := b.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestPricePriorityOverTime(t *testing.T) {
	b := NewBook()
	mustAdd(t, b, 1, Sell, 101_00, 5) // better time
	mustAdd(t, b, 2, Sell, 100_00, 5) // better price, later
	tr := mustAdd(t, b, 3, Buy, 102_00, 5)
	if len(tr) != 1 || tr[0].MakerID != 2 || tr[0].Price != 100_00 {
		t.Fatalf("best price must fill first: %+v", tr)
	}
}

func TestWalksTheBook(t *testing.T) {
	b := NewBook()
	mustAdd(t, b, 1, Sell, 100_00, 3)
	mustAdd(t, b, 2, Sell, 100_50, 3)
	mustAdd(t, b, 3, Sell, 101_00, 3)
	tr := mustAdd(t, b, 4, Buy, 100_75, 10) // eats 100.00 and 100.50, stops before 101.00
	if len(tr) != 2 {
		t.Fatalf("want 2 trades, got %d", len(tr))
	}
	if tr[0].Price != 100_00 || tr[1].Price != 100_50 {
		t.Fatalf("must walk in price order: %+v", tr)
	}
	bids, asks := b.Depth(5)
	if len(bids) != 1 || bids[0].Qty != 4 {
		t.Fatalf("remainder 4 must rest at 100.75: %+v", bids)
	}
	if asks[0].Price != 101_00 {
		t.Fatalf("101.00 ask must be untouched: %+v", asks)
	}
	if err := b.CheckInvariants(); err != nil {
		t.Fatal(err)
	}
}

func TestSellCrossesBids(t *testing.T) {
	b := NewBook()
	mustAdd(t, b, 1, Buy, 99_50, 5)
	mustAdd(t, b, 2, Buy, 100_00, 5)
	tr := mustAdd(t, b, 3, Sell, 99_75, 6)
	if len(tr) != 1 || tr[0].MakerID != 2 || tr[0].Price != 100_00 {
		t.Fatalf("must hit best bid 100.00 first: %+v", tr)
	}
	if b.Stats()["resting"] != 2 {
		t.Fatalf("order 3 remainder 1 + bid 1 should rest")
	}
}

func TestCancel(t *testing.T) {
	b := NewBook()
	mustAdd(t, b, 1, Buy, 100_00, 5)
	if err := b.Cancel(1); err != nil {
		t.Fatal(err)
	}
	if err := b.Cancel(1); err != ErrNoOrder {
		t.Fatal("double cancel must fail")
	}
	if b.Stats()["resting"] != 0 {
		t.Fatal("book must be empty")
	}
}

func TestMarketOrderIOC(t *testing.T) {
	b := NewBook()
	mustAdd(t, b, 1, Sell, 100_00, 5)
	tr, err := b.AddMarket(2, Buy, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(tr) != 1 || tr[0].Qty != 5 {
		t.Fatalf("market buy must take all 5: %+v", tr)
	}
	if b.Stats()["resting"] != 0 {
		t.Fatal("IOC remainder must not rest")
	}
}

func TestInvariantFuzz(t *testing.T) {
	// deterministic pseudo-random op mix; invariants must hold after every op
	b := NewBook()
	seed := int64(12345)
	rnd := func(n int64) int64 {
		seed = (seed*6364136223846793005 + 1442695040888963407)
		if seed < 0 {
			seed = -seed
		}
		return seed % n
	}
	var nextID uint64 = 1
	var resting []uint64
	for i := 0; i < 20000; i++ {
		op := rnd(100)
		switch {
		case op < 45: // add limit
			side := Buy
			px := 9900_00 + rnd(200_00)
			if rnd(2) == 0 {
				side = Sell
			}
			if _, err := b.AddLimit(nextID, side, px, 1+rnd(50)); err != nil {
				t.Fatalf("op %d: %v", i, err)
			}
			resting = append(resting, nextID)
			nextID++
		case op < 60 && len(resting) > 0: // cancel random
			j := rnd(int64(len(resting)))
			b.Cancel(resting[j])
			resting = append(resting[:j], resting[j+1:]...)
		default: // marketable taker
			side := Buy
			px := int64(10100_00)
			if rnd(2) == 0 {
				side = Sell
				px = 9800_00
			}
			b.AddLimit(nextID, side, px, 1+rnd(30))
			nextID++
		}
		if err := b.CheckInvariants(); err != nil {
			t.Fatalf("invariant after op %d: %v", i, err)
		}
	}
	t.Logf("fuzz done: %d orders, %d trades, %d resting",
		b.Stats()["orders_added"], b.Stats()["trades"], b.Stats()["resting"])
}
