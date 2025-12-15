package metrics

import (
	"strconv"
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
}

func NewTracker(cfg *config.Config) *Tracker {
	return &Tracker{
		MinTime:     time.Duration(1<<63 - 1), // Max duration
		MaxTime:     0,
		TotalCycles: cfg.TotalCycles,
		MsTimeProd:  cfg.MsTimeProduction,
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

	if t.BatchCount >= 100 {
		avgTime := t.TotalTime / time.Duration(t.CycleCount)

		logger.Info("Cycle Metrics (Last 100)",
			"duration_us", duration.Microseconds(),
			"min_us", t.MinTime.Microseconds(),
			"max_us", t.MaxTime.Microseconds(),
			"avg_us", avgTime.Microseconds(),
			"total_cycles", t.TotalCycles,
		)

		t.persistMetrics()
		t.BatchCount = 0
	}
}

func (t *Tracker) persistMetrics() {
	logger.Info("Persisting metrics to .env")

	err := config.UpdateEnvVariable("MS_TIME_PRODUCTION", strconv.FormatInt(t.MsTimeProd, 10))
	if err != nil {
		logger.Error("Failed to update MS_TIME_PRODUCTION", "error", err)
	}

	err = config.UpdateEnvVariable("TOTAL_CYCLES", strconv.FormatInt(t.TotalCycles, 10))
	if err != nil {
		logger.Error("Failed to update TOTAL_CYCLES", "error", err)
	}
}
