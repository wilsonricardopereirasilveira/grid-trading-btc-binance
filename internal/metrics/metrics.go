package metrics

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"grid-trading-btc-binance/internal/config"
	"grid-trading-btc-binance/internal/logger"
)

type Tracker struct {
	MinTime     time.Duration
	MaxTime     time.Duration
	TotalTime   time.Duration
	CycleCount  int64
	BatchCount  int
	TotalCycles int64
	MsTimeProd  int64
	StartTime   time.Time
	cfg         *config.Config
}

// MetricsPayload represents the JSON payload for the metrics API
type MetricsPayload struct {
	Strategy    string `json:"strategy"`
	Cycles      string `json:"cycles"`
	Min         string `json:"min"`
	Max         string `json:"max"`
	Avg         string `json:"avg"`
	Uptime      string `json:"uptime"`
	LastUpdated string `json:"lastUpdated"`
	Now         string `json:"now"`
}

func NewTracker(cfg *config.Config) *Tracker {
	return &Tracker{
		MinTime:     time.Duration(1<<63 - 1), // Max duration
		MaxTime:     0,
		TotalCycles: cfg.TotalCycles,
		MsTimeProd:  cfg.MsTimeProduction,
		StartTime:   time.Now(),
		cfg:         cfg,
	}
}

func (t *Tracker) TrackCycle(duration time.Duration) {
	t.CycleCount++
	t.TotalCycles++
	t.BatchCount++
	t.TotalTime += duration
	t.MsTimeProd += duration.Milliseconds()

	if duration < t.MinTime {
		t.MinTime = duration
	}
	if duration > t.MaxTime {
		t.MaxTime = duration
	}

	if t.BatchCount >= 5000 {
		avgTime := t.TotalTime / time.Duration(t.CycleCount)

		logger.Info("Cycle Metrics (Last 5000)",
			"duration_us", duration.Microseconds(),
			"min_us", t.MinTime.Microseconds(),
			"max_us", t.MaxTime.Microseconds(),
			"avg_us", avgTime.Microseconds(),
			"total_cycles", t.TotalCycles,
		)

		// Send metrics to external API
		t.sendMetricsToAPI(avgTime)

		t.persistMetrics()
		t.BatchCount = 0
	}
}

func (t *Tracker) sendMetricsToAPI(avgTime time.Duration) {
	if t.cfg.MetricsAPIURL == "" {
		return
	}

	// Calculate uptime in seconds
	uptime := int64(time.Since(t.StartTime).Seconds())

	// Get current time in GMT-3 (America/Sao_Paulo)
	loc := time.FixedZone("GMT-3", -3*60*60)
	now := time.Now().In(loc)
	lastUpdated := now.Format("2006-01-02T15:04:05.000Z")
	nowFormatted := now.Format("2006-01-02T15:04:05Z")

	// Convert microseconds to seconds (e.g., 100ms = 0.100 seconds)
	minSec := float64(t.MinTime.Microseconds()) / 1000000.0
	maxSec := float64(t.MaxTime.Microseconds()) / 1000000.0
	avgSec := float64(avgTime.Microseconds()) / 1000000.0

	payload := MetricsPayload{
		Strategy:    "grid-trading-bitcoin-binance",
		Cycles:      fmt.Sprintf("%d", t.TotalCycles),
		Min:         fmt.Sprintf("%.3f", minSec),
		Max:         fmt.Sprintf("%.3f", maxSec),
		Avg:         fmt.Sprintf("%.3f", avgSec),
		Uptime:      fmt.Sprintf("%d", uptime),
		LastUpdated: lastUpdated,
		Now:         nowFormatted,
	}

	jsonData, err := json.Marshal(payload)
	if err != nil {
		logger.Error("Failed to marshal metrics payload", "error", err)
		return
	}

	req, err := http.NewRequest("POST", t.cfg.MetricsAPIURL, bytes.NewBuffer(jsonData))
	if err != nil {
		logger.Error("Failed to create metrics API request", "error", err)
		return
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+t.cfg.MetricsAPIToken)

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		logger.Error("Failed to send metrics to API", "error", err)
		return
	}
	defer resp.Body.Close()
}

func (t *Tracker) persistMetrics() {
	// Persistence to .env removed per user request.
	// Metrics are now ephemeral or logged only.
}
