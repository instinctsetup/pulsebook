// cmd/bench measures matching-engine throughput: synthetic op mix (ns/op) and
// a replay of real Binance BTCUSDT aggTrades as market orders.
// Method: single goroutine, sequential calls, in-process API, fixed seed.
package main

import (
	"encoding/json"
	"fmt"
	"log"
	"math/rand"
	"os"
	"runtime"
	"time"

	"github.com/instinctsetup/pulsebook/pb"
)

const tick = 0.01     // BTCUSDT price tick
const qtyScale = 1e-6 // BTC lot

func main() {
	res := map[string]any{
		"platform": runtime.GOOS + "/" + runtime.GOARCH + ", Go " + runtime.Version(),
		"method":   "single goroutine, sequential calls, in-process engine API (no HTTP), fixed seed",
	}

	// --- 1. limit insert (non-crossing) ---
	b := pb.NewBook()
	const nIns = 300_000
	mid := int64(82000_00)
	start := time.Now()
	for i := 0; i < nIns; i++ {
		side := pb.Buy
		px := mid - 100 - int64(i%5000)
		if i%2 == 0 {
			side = pb.Sell
			px = mid + 100 + int64(i%5000)
		}
		b.AddLimit(uint64(i+1), side, px, 1000)
	}
	d := time.Since(start)
	res["limit_insert"] = fmt.Sprintf("%.0f ns/op (%.2fM orders/s)", float64(d.Nanoseconds())/nIns, float64(nIns)/d.Seconds()/1e6)

	// --- 2. cancel ---
	start = time.Now()
	for i := 0; i < nIns; i++ {
		b.Cancel(uint64(i + 1))
	}
	d = time.Since(start)
	res["cancel"] = fmt.Sprintf("%.0f ns/op (%.2fM cancels/s)", float64(d.Nanoseconds())/nIns, float64(nIns)/d.Seconds()/1e6)

	// --- 3. matching: incoming orders crossing a seeded book ---
	b2 := pb.NewBook()
	rnd := rand.New(rand.NewSource(7))
	var id uint64 = 1
	for i := 0; i < 2000; i++ {
		b2.AddLimit(id, pb.Buy, mid-1-int64(rnd.Intn(500)), 500+int64(rnd.Intn(2000)))
		id++
		b2.AddLimit(id, pb.Sell, mid+1+int64(rnd.Intn(500)), 500+int64(rnd.Intn(2000)))
		id++
	}
	const nMatch = 200_000
	var trades int64
	start = time.Now()
	for i := 0; i < nMatch; i++ {
		// taker crosses the touch; maker refill keeps the book alive
		if i%2 == 0 {
			t, _ := b2.AddLimit(id, pb.Buy, mid+1+int64(rnd.Intn(200)), 300)
			trades += int64(len(t))
		} else {
			t, _ := b2.AddLimit(id, pb.Sell, mid-1-int64(rnd.Intn(200)), 300)
			trades += int64(len(t))
		}
		id++
		b2.AddLimit(id, pb.Buy, mid-1-int64(rnd.Intn(50)), 300)
		id++
		b2.AddLimit(id, pb.Sell, mid+1+int64(rnd.Intn(50)), 300)
		id++
	}
	d = time.Since(start)
	res["matching_ops"] = fmt.Sprintf("%.0f ns/op (%.2fM orders/s, %d trades executed)", float64(d.Nanoseconds())/nMatch, float64(nMatch)/d.Seconds()/1e6, trades)

	// --- 4. real Binance replay ---
	path := "data/BTCUSDT-aggTrades-2026-10-19.csv"
	if p := os.Getenv("PULSEBOOK_DATA"); p != "" {
		path = p
	}
	rows, err := pb.LoadBinanceAggTrades(path, 100_000)
	if err != nil {
		log.Printf("binance data: %v (skipping replay)", err)
	} else {
		b3 := pb.NewBook()
		var rid uint64 = 1
		start = time.Now()
		exec, err := pb.ReplayBinance(b3, rows, tick, qtyScale, &rid)
		d = time.Since(start)
		if err != nil {
			log.Printf("replay: %v", err)
		}
		if err := b3.CheckInvariants(); err != nil {
			log.Fatalf("INVARIANT VIOLATION after real-data replay: %v", err)
		}
		res["binance_replay"] = fmt.Sprintf("%s real BTCUSDT aggressor trades (2026-10-19) replayed in %.2fs = %.2fM events/s, %d executions, invariants held",
			comma(len(rows)), d.Seconds(), float64(rid-1)/d.Seconds()/1e6, exec)
		res["binance_source"] = "https://data.binance.vision/data/spot/daily/aggTrades/BTCUSDT/BTCUSDT-aggTrades-2026-10-19.zip"
	}

	out, _ := json.MarshalIndent(res, "", "  ")
	fmt.Println(string(out))
	os.WriteFile("benchmarks.json", out, 0644)
}

func comma(n int) string {
	s := fmt.Sprintf("%d", n)
	var out []byte
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			out = append(out, ',')
		}
		out = append(out, byte(c))
	}
	return string(out)
}
