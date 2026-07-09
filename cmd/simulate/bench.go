package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/urfave/cli/v3"
)

func bench(ctx context.Context, cmd *cli.Command) error {
	serverURL := strings.TrimSuffix(cmd.String("server"), "/")
	endpoint := serverURL + "/rgeocode/multiaddress"

	numPoints := int(cmd.Int("count"))
	workers := int(cmd.Int("workers"))
	repeat := int(cmd.Int("repeat"))
	minLat := cmd.Float64("min-lat")
	maxLat := cmd.Float64("max-lat")
	minLon := cmd.Float64("min-lon")
	maxLon := cmd.Float64("max-lon")
	timeout := cmd.Duration("timeout")
	if timeout == 0 {
		timeout = defaultTimeout
	}

	if minLat >= maxLat || minLon >= maxLon {
		return fmt.Errorf("invalid bounding box: min must be less than max")
	}
	if numPoints <= 0 || workers <= 0 || repeat <= 0 {
		return fmt.Errorf("count, workers, and repeat must be positive")
	}

	client := &http.Client{
		Timeout: timeout,
		Transport: &http.Transport{
			MaxIdleConnsPerHost: workers,
			MaxConnsPerHost:     workers,
		},
	}

	var (
		successCount atomic.Int64
		failureCount atomic.Int64
		totalPoints  atomic.Int64
		latenciesUs  []int64
		latenciesMu  sync.Mutex
	)

	fmt.Printf("Simulating load on %s\n", serverURL)
	fmt.Printf("  Points per request: %d\n", numPoints)
	fmt.Printf("  Workers:            %d\n", workers)
	fmt.Printf("  Total requests:     %d\n", repeat)
	fmt.Printf("  Bounding box:       lat [%.4f, %.4f], lon [%.4f, %.4f]\n", minLat, maxLat, minLon, maxLon)
	fmt.Println()

	startTime := time.Now()

	workCh := make(chan int, repeat)
	for i := range repeat {
		workCh <- i
	}
	close(workCh)

	var wg sync.WaitGroup
	for w := range workers {
		wg.Add(1)
		go func(workerID int) {
			defer wg.Done()
			for range workCh {
				points := generatePoints(numPoints, minLat, maxLat, minLon, maxLon)
				body, err := json.Marshal(points)
				if err != nil {
					log.Printf("worker %d: marshal error: %v", workerID, err)
					failureCount.Add(1)
					continue
				}

				reqStart := time.Now()
				resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
				latency := time.Since(reqStart).Microseconds()

				latenciesMu.Lock()
				latenciesUs = append(latenciesUs, latency)
				latenciesMu.Unlock()

				if err != nil {
					log.Printf("worker %d: request error: %v", workerID, err)
					failureCount.Add(1)
					continue
				}

				_, _ = io.Copy(io.Discard, resp.Body)
				resp.Body.Close()

				if resp.StatusCode == http.StatusOK {
					successCount.Add(1)
					totalPoints.Add(int64(numPoints))
				} else {
					failureCount.Add(1)
					log.Printf("worker %d: HTTP %d", workerID, resp.StatusCode)
				}
			}
		}(w)
	}

	wg.Wait()
	elapsed := time.Since(startTime)

	// Report.
	total := successCount.Load() + failureCount.Load()
	fmt.Println("=== Results ===")
	fmt.Printf("Duration:          %s\n", elapsed.Round(time.Millisecond))
	fmt.Printf("Requests:          %d ok / %d fail / %d total\n",
		successCount.Load(), failureCount.Load(), total)
	fmt.Printf("Points queried:    %d\n", totalPoints.Load())
	if elapsed.Seconds() > 0 && total > 0 {
		fmt.Printf("Throughput:        %.1f req/s, %.1f points/s\n",
			float64(total)/elapsed.Seconds(), float64(totalPoints.Load())/elapsed.Seconds())
	}

	if len(latenciesUs) > 0 {
		slices.Sort(latenciesUs)

		p50 := latenciesUs[len(latenciesUs)*50/100]
		p95 := latenciesUs[len(latenciesUs)*95/100]
		p99 := latenciesUs[len(latenciesUs)*99/100]
		minL := latenciesUs[0]
		maxL := latenciesUs[len(latenciesUs)-1]

		var sum int64
		for _, l := range latenciesUs {
			sum += l
		}
		avg := sum / int64(len(latenciesUs))

		fmt.Println()
		fmt.Println("=== Latency ===")
		fmt.Printf("Min:     %s\n", time.Duration(minL)*time.Microsecond)
		fmt.Printf("Avg:     %s\n", time.Duration(avg)*time.Microsecond)
		fmt.Printf("P50:     %s\n", time.Duration(p50)*time.Microsecond)
		fmt.Printf("P95:     %s\n", time.Duration(p95)*time.Microsecond)
		fmt.Printf("P99:     %s\n", time.Duration(p99)*time.Microsecond)
		fmt.Printf("Max:     %s\n", time.Duration(maxL)*time.Microsecond)
	}

	return nil
}
