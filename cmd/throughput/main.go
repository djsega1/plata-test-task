// Command throughput fires concurrent POST /quotes/updates requests at a
// running server for a fixed duration and reports latency percentiles and
// status-code counts. Stdlib only — a one-off measurement tool, not a
// service dependency.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
)

type result struct {
	latency time.Duration
	status  int
	err     error
}

func main() {
	baseURL := flag.String("url", "http://localhost:8080", "server base URL")
	pairsFlag := flag.String("pairs", "USD/EUR,USD/MXN,EUR/MXN,EUR/USD,MXN/USD,MXN/EUR", "comma-separated pairs to cycle through")
	concurrency := flag.Int("concurrency", 50, "number of concurrent workers")
	duration := flag.Duration("duration", 20*time.Second, "how long to generate load")
	flag.Parse()

	pairs := strings.Split(*pairsFlag, ",")
	client := &http.Client{Timeout: 10 * time.Second}

	ctx, cancel := context.WithTimeout(context.Background(), *duration)
	defer cancel()

	results := make(chan result, 4*(*concurrency))
	var wg sync.WaitGroup
	for w := range *concurrency {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for n := 0; ; n++ {
				select {
				case <-ctx.Done():
					return
				default:
				}
				pair := pairs[(worker+n)%len(pairs)]
				results <- doPost(ctx, client, *baseURL, pair)
			}
		}(w)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	var latencies []time.Duration
	okCount, errCount := 0, 0
	statusCounts := map[int]int{}
	for r := range results {
		if r.err != nil {
			errCount++
			continue
		}
		latencies = append(latencies, r.latency)
		statusCounts[r.status]++
		if r.status < 400 {
			okCount++
		} else {
			errCount++
		}
	}

	report(*duration, latencies, okCount, errCount, statusCounts)
}

func doPost(ctx context.Context, client *http.Client, baseURL, pair string) result {
	body, err := json.Marshal(map[string]string{"pair": pair})
	if err != nil {
		return result{err: err}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/api/v1/quotes/updates", bytes.NewReader(body))
	if err != nil {
		return result{err: err}
	}
	req.Header.Set("Content-Type", "application/json")

	start := time.Now()
	resp, err := client.Do(req)
	return finish(start, resp, err)
}

func finish(start time.Time, resp *http.Response, err error) result {
	if err != nil {
		return result{err: err}
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return result{latency: time.Since(start), status: resp.StatusCode}
}

func report(duration time.Duration, latencies []time.Duration, ok, errs int, statusCounts map[int]int) {
	total := ok + errs
	fmt.Printf("requests: %d (ok=%d, errors=%d) over %s -> %.1f req/s\n", total, ok, errs, duration, float64(total)/duration.Seconds())

	if len(latencies) == 0 {
		fmt.Println("no successful responses to compute latency percentiles from")
		return
	}
	slices.Sort(latencies)
	pct := func(p float64) time.Duration {
		idx := int(p * float64(len(latencies)-1))
		return latencies[idx]
	}
	fmt.Printf("latency: min=%s p50=%s p90=%s p99=%s max=%s\n",
		latencies[0], pct(0.50), pct(0.90), pct(0.99), latencies[len(latencies)-1])

	fmt.Println("status codes:")
	codes := make([]int, 0, len(statusCounts))
	for code := range statusCounts {
		codes = append(codes, code)
	}
	sort.Ints(codes)
	for _, code := range codes {
		fmt.Printf("  %d: %d\n", code, statusCounts[code])
	}
}
