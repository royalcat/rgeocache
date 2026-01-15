package stats

import (
	"fmt"
	"os"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/process"
)

// RuntimeStats holds all collected runtime statistics
type RuntimeStats struct {
	StartTime    time.Time          `json:"start_time"`
	EndTime      time.Time          `json:"end_time"`
	TotalElapsed time.Duration      `json:"total_elapsed_ns"`
	ElapsedHuman string             `json:"total_elapsed"`
	Samples      []RuntimeStatPoint `json:"samples"`
	Summary      StatsSummary       `json:"summary"`
}

// RuntimeStatPoint represents a single sample of runtime stats
type RuntimeStatPoint struct {
	Timestamp      time.Time `json:"timestamp"`
	ElapsedSeconds float64   `json:"elapsed_seconds"`
	// Memory stats (in bytes)
	HeapAlloc       uint64 `json:"heap_alloc"`
	HeapSys         uint64 `json:"heap_sys"`
	HeapInuse       uint64 `json:"heap_inuse"`
	StackInuse      uint64 `json:"stack_inuse"`
	TotalAlloc      uint64 `json:"total_alloc"`
	Sys             uint64 `json:"sys"`
	NumGC           uint32 `json:"num_gc"`
	ProcessRSSBytes uint64 `json:"process_rss_bytes"`
	// CPU stats
	CPUPercent   float64   `json:"cpu_percent"`
	SystemCPU    []float64 `json:"system_cpu_percent"`
	NumGoroutine int       `json:"num_goroutine"`
}

// StatsSummary contains summary statistics
type StatsSummary struct {
	PeakHeapAlloc    uint64  `json:"peak_heap_alloc"`
	PeakHeapSys      uint64  `json:"peak_heap_sys"`
	PeakSys          uint64  `json:"peak_sys"`
	PeakProcessRSS   uint64  `json:"peak_process_rss"`
	PeakCPUPercent   float64 `json:"peak_cpu_percent"`
	AvgCPUPercent    float64 `json:"avg_cpu_percent"`
	PeakGoroutines   int     `json:"peak_goroutines"`
	TotalGCCycles    uint32  `json:"total_gc_cycles"`
	SampleCount      int     `json:"sample_count"`
	SampleIntervalMs int64   `json:"sample_interval_ms"`
}

// Collector collects runtime statistics over time
type Collector struct {
	mu        sync.Mutex
	stats     RuntimeStats
	startTime time.Time
	stopChan  chan struct{}
	doneChan  chan struct{}
	interval  time.Duration
	proc      *process.Process
}

// NewCollector creates a new stats collector
func NewCollector(interval time.Duration) (*Collector, error) {
	proc, err := process.NewProcess(int32(os.Getpid()))
	if err != nil {
		return nil, fmt.Errorf("failed to get process info: %w", err)
	}

	return &Collector{
		stats: RuntimeStats{
			Samples: make([]RuntimeStatPoint, 0, 1000),
		},
		interval: interval,
		stopChan: make(chan struct{}),
		doneChan: make(chan struct{}),
		proc:     proc,
	}, nil
}

// Start begins collecting statistics
func (c *Collector) Start() {
	c.startTime = time.Now()
	c.stats.StartTime = c.startTime

	go c.collect()
}

// collect runs the collection loop
func (c *Collector) collect() {
	defer close(c.doneChan)

	ticker := time.NewTicker(c.interval)
	defer ticker.Stop()

	// Collect initial sample
	c.sample()

	for {
		select {
		case <-c.stopChan:
			// Collect final sample
			c.sample()
			return
		case <-ticker.C:
			c.sample()
		}
	}
}

// sample collects a single sample of runtime stats
func (c *Collector) sample() {
	var memStats runtime.MemStats
	runtime.ReadMemStats(&memStats)

	elapsed := time.Since(c.startTime)

	point := RuntimeStatPoint{
		Timestamp:      time.Now(),
		ElapsedSeconds: elapsed.Seconds(),
		HeapAlloc:      memStats.HeapAlloc,
		HeapSys:        memStats.HeapSys,
		HeapInuse:      memStats.HeapInuse,
		StackInuse:     memStats.StackInuse,
		TotalAlloc:     memStats.TotalAlloc,
		Sys:            memStats.Sys,
		NumGC:          memStats.NumGC,
		NumGoroutine:   runtime.NumGoroutine(),
	}

	// Get process RSS
	if memInfo, err := c.proc.MemoryInfo(); err == nil && memInfo != nil {
		point.ProcessRSSBytes = memInfo.RSS
	}

	// Get CPU percent for this process
	if cpuPercent, err := c.proc.CPUPercent(); err == nil {
		point.CPUPercent = cpuPercent
	}

	// Get system CPU percent
	if systemCPU, err := cpu.Percent(0, true); err == nil {
		point.SystemCPU = systemCPU
	}

	c.mu.Lock()
	c.stats.Samples = append(c.stats.Samples, point)
	c.mu.Unlock()
}

// Stop stops collecting and returns the final stats
func (c *Collector) Stop() RuntimeStats {
	close(c.stopChan)
	<-c.doneChan

	c.mu.Lock()
	defer c.mu.Unlock()

	c.stats.EndTime = time.Now()
	c.stats.TotalElapsed = c.stats.EndTime.Sub(c.stats.StartTime)
	c.stats.ElapsedHuman = c.stats.TotalElapsed.String()

	// Calculate summary
	c.calculateSummary()

	return c.stats
}

// calculateSummary computes summary statistics from all samples
func (c *Collector) calculateSummary() {
	if len(c.stats.Samples) == 0 {
		return
	}

	var totalCPU float64

	for _, s := range c.stats.Samples {
		if s.HeapAlloc > c.stats.Summary.PeakHeapAlloc {
			c.stats.Summary.PeakHeapAlloc = s.HeapAlloc
		}
		if s.HeapSys > c.stats.Summary.PeakHeapSys {
			c.stats.Summary.PeakHeapSys = s.HeapSys
		}
		if s.Sys > c.stats.Summary.PeakSys {
			c.stats.Summary.PeakSys = s.Sys
		}
		if s.ProcessRSSBytes > c.stats.Summary.PeakProcessRSS {
			c.stats.Summary.PeakProcessRSS = s.ProcessRSSBytes
		}
		if s.CPUPercent > c.stats.Summary.PeakCPUPercent {
			c.stats.Summary.PeakCPUPercent = s.CPUPercent
		}
		if s.NumGoroutine > c.stats.Summary.PeakGoroutines {
			c.stats.Summary.PeakGoroutines = s.NumGoroutine
		}
		if s.NumGC > c.stats.Summary.TotalGCCycles {
			c.stats.Summary.TotalGCCycles = s.NumGC
		}
		totalCPU += s.CPUPercent
	}

	c.stats.Summary.SampleCount = len(c.stats.Samples)
	c.stats.Summary.SampleIntervalMs = c.interval.Milliseconds()
	if c.stats.Summary.SampleCount > 0 {
		c.stats.Summary.AvgCPUPercent = totalCPU / float64(c.stats.Summary.SampleCount)
	}
}

// SaveToFile saves the stats to a human-readable text file
func (stats *RuntimeStats) SaveToFile(filename string) error {
	var sb strings.Builder

	sb.WriteString("================================================================================\n")
	sb.WriteString("                         RUNTIME STATISTICS REPORT\n")
	sb.WriteString("================================================================================\n\n")

	// Time information
	sb.WriteString("TIME INFORMATION\n")
	sb.WriteString("--------------------------------------------------------------------------------\n")
	sb.WriteString(fmt.Sprintf("  Start Time:      %s\n", stats.StartTime.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("  End Time:        %s\n", stats.EndTime.Format(time.RFC3339)))
	sb.WriteString(fmt.Sprintf("  Total Duration:  %s\n", stats.ElapsedHuman))
	sb.WriteString("\n")

	// Summary statistics
	sb.WriteString("SUMMARY\n")
	sb.WriteString("--------------------------------------------------------------------------------\n")
	sb.WriteString(fmt.Sprintf("  Sample Count:        %d samples\n", stats.Summary.SampleCount))
	sb.WriteString(fmt.Sprintf("  Sample Interval:     %d ms\n", stats.Summary.SampleIntervalMs))
	sb.WriteString("\n")

	sb.WriteString("  Memory (Peak Values):\n")
	sb.WriteString(fmt.Sprintf("    Heap Allocated:    %s\n", formatBytes(stats.Summary.PeakHeapAlloc)))
	sb.WriteString(fmt.Sprintf("    Heap System:       %s\n", formatBytes(stats.Summary.PeakHeapSys)))
	sb.WriteString(fmt.Sprintf("    Total System:      %s\n", formatBytes(stats.Summary.PeakSys)))
	sb.WriteString(fmt.Sprintf("    Process RSS:       %s\n", formatBytes(stats.Summary.PeakProcessRSS)))
	sb.WriteString("\n")

	sb.WriteString("  CPU:\n")
	sb.WriteString(fmt.Sprintf("    Peak CPU:          %.2f%%\n", stats.Summary.PeakCPUPercent))
	sb.WriteString(fmt.Sprintf("    Average CPU:       %.2f%%\n", stats.Summary.AvgCPUPercent))
	sb.WriteString("\n")

	sb.WriteString("  Other:\n")
	sb.WriteString(fmt.Sprintf("    Peak Goroutines:   %d\n", stats.Summary.PeakGoroutines))
	sb.WriteString(fmt.Sprintf("    Total GC Cycles:   %d\n", stats.Summary.TotalGCCycles))
	sb.WriteString("\n")

	// Detailed samples header
	sb.WriteString("DETAILED SAMPLES\n")
	sb.WriteString("--------------------------------------------------------------------------------\n")

	// Limit samples to maxSamples evenly distributed across the collection
	const maxSamples = 100
	samplesToOutput := stats.Samples
	if len(stats.Samples) > maxSamples {
		samplesToOutput = make([]RuntimeStatPoint, 0, maxSamples)
		step := float64(len(stats.Samples)-1) / float64(maxSamples-1)
		for i := 0; i < maxSamples; i++ {
			idx := int(float64(i) * step)
			samplesToOutput = append(samplesToOutput, stats.Samples[idx])
		}
		sb.WriteString(fmt.Sprintf("  (Showing %d of %d samples, evenly distributed)\n\n", maxSamples, len(stats.Samples)))
	}

	sb.WriteString(fmt.Sprintf("%-12s %-14s %-14s %-14s %-10s %-10s\n",
		"Elapsed(s)", "Heap Alloc", "Process RSS", "Sys Memory", "CPU %", "Goroutines"))
	sb.WriteString(fmt.Sprintf("%-12s %-14s %-14s %-14s %-10s %-10s\n",
		"----------", "----------", "-----------", "----------", "-----", "----------"))

	// Output samples
	for _, sample := range samplesToOutput {
		sb.WriteString(fmt.Sprintf("%-12.1f %-14s %-14s %-14s %-10.1f %-10d\n",
			sample.ElapsedSeconds,
			formatBytes(sample.HeapAlloc),
			formatBytes(sample.ProcessRSSBytes),
			formatBytes(sample.Sys),
			sample.CPUPercent,
			sample.NumGoroutine))
	}

	sb.WriteString("\n================================================================================\n")
	sb.WriteString("                              END OF REPORT\n")
	sb.WriteString("================================================================================\n")

	if err := os.WriteFile(filename, []byte(sb.String()), 0644); err != nil {
		return fmt.Errorf("failed to write stats file: %w", err)
	}

	return nil
}

// formatBytes converts bytes to a human-readable string
func formatBytes(bytes uint64) string {
	const (
		KB = 1024
		MB = KB * 1024
		GB = MB * 1024
	)

	switch {
	case bytes >= GB:
		return fmt.Sprintf("%.2f GB", float64(bytes)/float64(GB))
	case bytes >= MB:
		return fmt.Sprintf("%.2f MB", float64(bytes)/float64(MB))
	case bytes >= KB:
		return fmt.Sprintf("%.2f KB", float64(bytes)/float64(KB))
	default:
		return fmt.Sprintf("%d B", bytes)
	}
}
