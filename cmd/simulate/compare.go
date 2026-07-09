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

	"github.com/royalcat/rgeocache/geomodel"
	"github.com/urfave/cli/v3"
)

func compare(ctx context.Context, cmd *cli.Command) error {
	serverA := strings.TrimSuffix(cmd.String("server-a"), "/")
	serverB := strings.TrimSuffix(cmd.String("server-b"), "/")
	endpointA := serverA + "/rgeocode/multiaddress"
	endpointB := serverB + "/rgeocode/multiaddress"

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

		matchCount    atomic.Int64
		mismatchCount atomic.Int64
		mismatches    []mismatchEntry
		mismatchesMu  sync.Mutex
	)

	fmt.Printf("Comparing %s (A) vs %s (B)\n", serverA, serverB)
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

				respA, bodyA, errA := doPost(client, body, endpointA, &latenciesUs, &latenciesMu)
				respB, bodyB, errB := doPost(client, body, endpointB, &latenciesUs, &latenciesMu)

				if errA != nil || errB != nil {
					failureCount.Add(1)
					if errA != nil {
						log.Printf("worker %d: server A error: %v", workerID, errA)
					}
					if errB != nil {
						log.Printf("worker %d: server B error: %v", workerID, errB)
					}
					closeRespBody(respA)
					closeRespBody(respB)
					continue
				}

				okA := respA.StatusCode == http.StatusOK
				okB := respB.StatusCode == http.StatusOK

				if !okA || !okB {
					failureCount.Add(1)
					if !okA {
						log.Printf("worker %d: server A: HTTP %d", workerID, respA.StatusCode)
					}
					if !okB {
						log.Printf("worker %d: server B: HTTP %d", workerID, respB.StatusCode)
					}
					continue
				}

				successCount.Add(1)
				totalPoints.Add(int64(numPoints))

				// Compare responses.
				var listA, listB geomodel.InfoList
				if err := json.Unmarshal(bodyA, &listA); err != nil {
					log.Printf("worker %d: failed to unmarshal response from A: %v", workerID, err)
					failureCount.Add(1)
					continue
				}
				if err := json.Unmarshal(bodyB, &listB); err != nil {
					log.Printf("worker %d: failed to unmarshal response from B: %v", workerID, err)
					failureCount.Add(1)
					continue
				}

				n := min(len(listA), len(listB), len(points))
				for i := range n {
					diffs := compareInfo(listA[i], listB[i])
					if len(diffs) == 0 {
						matchCount.Add(1)
					} else {
						mismatchCount.Add(1)
						mismatchesMu.Lock()
						mismatches = append(mismatches, mismatchEntry{
							Lat:    points[i][0],
							Lon:    points[i][1],
							Detail: strings.Join(diffs, "; "),
						})
						mismatchesMu.Unlock()
					}
				}

				// Handle length mismatches.
				if len(listA) != len(listB) {
					extra := abs(len(listA) - len(listB))
					mismatchCount.Add(int64(extra))
					mismatchesMu.Lock()
					mismatches = append(mismatches, mismatchEntry{
						Lat:    0,
						Lon:    0,
						Detail: fmt.Sprintf("response length: A=%d, B=%d", len(listA), len(listB)),
					})
					mismatchesMu.Unlock()
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

	fmt.Println()
	fmt.Println("=== Comparison ===")
	matched := matchCount.Load()
	mismatched := mismatchCount.Load()
	totalCmp := matched + mismatched
	fmt.Printf("Points matched:    %d\n", matched)
	fmt.Printf("Points mismatched: %d\n", mismatched)
	if totalCmp > 0 {
		fmt.Printf("Match rate:        %.2f%%\n", float64(matched)/float64(totalCmp)*100)
	}

	mismatchesMu.Lock()
	if len(mismatches) > 0 {
		maxShow := 50
		fmt.Printf("\nMismatch details (showing up to %d):\n", maxShow)
		for i, m := range mismatches {
			if i >= maxShow {
				fmt.Printf("  ... and %d more mismatches\n", len(mismatches)-maxShow)
				break
			}
			if m.Lat == 0 && m.Lon == 0 {
				fmt.Printf("  [%d] %s\n", i+1, m.Detail)
			} else {
				fmt.Printf("  [%d] (%.6f, %.6f): %s\n", i+1, m.Lat, m.Lon, m.Detail)
			}
		}
	}
	mismatchesMu.Unlock()

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
		fmt.Println("=== Latency (combined) ===")
		fmt.Printf("Min:     %s\n", time.Duration(minL)*time.Microsecond)
		fmt.Printf("Avg:     %s\n", time.Duration(avg)*time.Microsecond)
		fmt.Printf("P50:     %s\n", time.Duration(p50)*time.Microsecond)
		fmt.Printf("P95:     %s\n", time.Duration(p95)*time.Microsecond)
		fmt.Printf("P99:     %s\n", time.Duration(p99)*time.Microsecond)
		fmt.Printf("Max:     %s\n", time.Duration(maxL)*time.Microsecond)
	}

	return nil
}

type mismatchEntry struct {
	Lat    float64
	Lon    float64
	Detail string
}

func doPost(client *http.Client, body []byte, endpoint string, latenciesUs *[]int64, latenciesMu *sync.Mutex) (*http.Response, []byte, error) {
	reqStart := time.Now()
	resp, err := client.Post(endpoint, "application/json", bytes.NewReader(body))
	latency := time.Since(reqStart).Microseconds()

	latenciesMu.Lock()
	*latenciesUs = append(*latenciesUs, latency)
	latenciesMu.Unlock()

	if err != nil {
		return nil, nil, err
	}

	respBody, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		return resp, nil, err
	}

	return resp, respBody, nil
}

func closeRespBody(resp *http.Response) {
	if resp != nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
	}
}

func compareInfo(a, b geomodel.Info) []string {
	var diffs []string

	if a.Name != b.Name {
		diffs = append(diffs, fmt.Sprintf("name: %q vs %q", a.Name, b.Name))
	}
	if a.Street != b.Street {
		diffs = append(diffs, fmt.Sprintf("street: %q vs %q", a.Street, b.Street))
	}
	if a.HouseNumber != b.HouseNumber {
		diffs = append(diffs, fmt.Sprintf("house_number: %q vs %q", a.HouseNumber, b.HouseNumber))
	}
	if a.City != b.City {
		diffs = append(diffs, fmt.Sprintf("city: %q vs %q", a.City, b.City))
	}
	if a.Region != b.Region {
		diffs = append(diffs, fmt.Sprintf("region: %q vs %q", a.Region, b.Region))
	}
	if a.Country != b.Country {
		diffs = append(diffs, fmt.Sprintf("country: %q vs %q", a.Country, b.Country))
	}
	if a.Weight != b.Weight {
		diffs = append(diffs, fmt.Sprintf("weight: %d vs %d", a.Weight, b.Weight))
	}

	return diffs
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
