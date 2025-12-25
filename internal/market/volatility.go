package market

import (
	"fmt"
	"math"
	"strconv"
	"sync"
	"time"

	"grid-trading-btc-binance/internal/api"
	"grid-trading-btc-binance/internal/config"
	"grid-trading-btc-binance/internal/logger"
)

type VolatilityService struct {
	Cfg     *config.Config
	Binance *api.BinanceClient

	// State
	currentVol float64
	multiplier float64
	lastUpdate time.Time
	mu         sync.RWMutex
}

func NewVolatilityService(cfg *config.Config, binance *api.BinanceClient) *VolatilityService {
	return &VolatilityService{
		Cfg:        cfg,
		Binance:    binance,
		multiplier: cfg.LowVolMultiplier, // Default to Low Vol Multiplier (Normal Regime)
	}
}

// StartPolling begins the background loop to fetch candles and update volatility
func (s *VolatilityService) StartPolling() {
	go func() {
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		// Initial Run
		s.UpdateVolatility()

		for range ticker.C {
			s.UpdateVolatility()
		}
	}()
}

// UpdateVolatility fetches 1m candles and calculates Garman-Klass Volatility + Regime
func (s *VolatilityService) UpdateVolatility() {
	// We need lookback for Long Term (20) + some buffer. Let's get 30 candles.
	klines, err := s.Binance.GetRecentKlines(s.Cfg.Symbol, "1m", 30)
	if err != nil {
		logger.Error("⚠️ VolatilityService: Failed to fetch klines", "error", err)
		return
	}

	if len(klines) < 20 {
		logger.Warn("⚠️ VolatilityService: Not enough klines for calculation", "count", len(klines))
		return
	}

	// Calculate GK Volatility (Annualized? Or Per Period? Usually per period for Spacing)
	// We want the volatility of the PRICE itself to determine spacing.
	// GK gives Variance -> Volatility.

	// 1. Calculate Short Term Volatility (Last 5 mins)
	shortVol := s.calculateGK(klines[len(klines)-5:])

	// 2. Calculate Long Term Volatility (Last 20 mins)
	longVol := s.calculateGK(klines[len(klines)-20:])

	// 3. Regime Detection
	// If Short > Long * 1.5 -> Acceleration/Crash -> High Vol Multiplier
	// Fix: Added Threshold > 0.002 (0.2%) to avoid Low Volatility Noise triggering Crash Mode
	var newMultiplier float64
	var regime string

	if longVol > 0 && shortVol > (longVol*1.5) && shortVol > 0.002 {
		newMultiplier = s.Cfg.HighVolMultiplier
		regime = "HIGH_VOL_CRASH"
	} else {
		newMultiplier = s.Cfg.LowVolMultiplier
		regime = "NORMAL"
	}

	s.mu.Lock()
	s.currentVol = shortVol // Use short term vol as base? Or just use the multiplier logic on base spacing?
	// User Prompt:
	// "Substituir o GRID_SPACING_PCT fixo por um cálculo dinâmico de volatilidade usando o estimador Garman-Klass"
	// "Se Curta > Longa * 1.5 ... Usar HIGH_VOL_MULTIPLIER (ex: 3.5x) para abrir o grid."
	// Interpretation: The spacing IS Dynamic. calculating exact spacing vs just multiplier.
	// Usually: Spacing = Volatility * Multiplier.
	// If Volatility is e.g. 0.001 (0.1%), and Multiplier is 1.8 -> Spacing = 0.18%.
	// If Crash, Vol might be 0.005 (0.5%) and Multiplier 3.5 -> Spacing = 1.75%.
	// This fits "Opening the grid".

	s.multiplier = newMultiplier
	s.lastUpdate = time.Now()
	s.mu.Unlock()

	logger.Info("📊 Volatility Update (Garman-Klass)",
		"short_vol", shortVol,
		"long_vol", longVol,
		"regime", regime,
		"multiplier", newMultiplier,
	)
}

// CalculateGK calculates Garman-Klass Volatility for a given slice of Klines
// Formula: sigma^2 = 0.5 * (ln(High/Low))^2 - (2*ln(2) - 1) * (ln(Close/Open))^2
// Returns sqrt(sigma^2) i.e. volatility
func (s *VolatilityService) calculateGK(klines []api.Kline) float64 {
	var sumSigmaSq float64

	cons := 2.0*math.Log(2.0) - 1.0 // approx 0.386

	count := 0
	for _, k := range klines {
		o, _ := strconv.ParseFloat(k.Open, 64)
		h, _ := strconv.ParseFloat(k.High, 64)
		l, _ := strconv.ParseFloat(k.Low, 64)
		c, _ := strconv.ParseFloat(k.Close, 64)

		if o == 0 || l == 0 {
			continue // Avoid division by zero
		}

		// Terms
		term1 := math.Pow(math.Log(h/l), 2)
		term2 := math.Pow(math.Log(c/o), 2)

		sigmaSq := 0.5*term1 - cons*term2
		sumSigmaSq += sigmaSq
		count++
	}

	if count == 0 {
		return 0
	}

	avgSigmaSq := sumSigmaSq / float64(count)
	return math.Sqrt(avgSigmaSq)
}

// GetDynamicSpacing calculates the required grid spacing based on current market conditions
// Returns a Percentage (e.g. 0.005 for 0.5%)
func (s *VolatilityService) GetDynamicSpacing() float64 {
	s.mu.RLock()
	defer s.mu.RUnlock()

	// Safety: If no data yet, return configured safe fallback (e.g. 0.3% fixed)
	if s.currentVol == 0 {
		return s.Cfg.GridSpacingPct // Fallback to fixed config
	}

	// Logig: Dynamic Spacing = Volatility * Multiplier
	// Verify if Volatility is "per candle". Yes, calculated above per 1m candle.
	// If 1m vol is 0.05%, spacing = 0.05% * 1.8 = 0.09%.
	// This scales naturally.

	// Minimum Floor?
	// If volatility is extremely low, we don't want spacing to be 0.0001%.
	// Maybe clamp to 0.1% min?
	// User didn't specify, but good practice.
	// Let's use GridSpacingPct from env as floor? Or just raw?
	// Let's stick to pure math first.

	spacing := s.currentVol * s.multiplier

	// SAFETY: Min Spacing 0.1% (0.001) to avoid fee death
	if spacing < 0.001 {
		spacing = 0.001
	}

	return spacing
}

// GetMetrics returns the current internal state for logging/reporting
func (s *VolatilityService) GetMetrics() (shortVol, multiplier float64) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.currentVol, s.multiplier
}

// GetLastHourRange fetches the High and Low prices of the last 1h candle to estimate volatility/drawdown
func (s *VolatilityService) GetLastHourRange() (high, low float64, err error) {
	// Fetch last 1 candle of 1h interval
	// Note: Binance returns the *current* open candle if we ask for recent.
	// This captures the "High/Low" of the current hour so far, which matches our snapshot timestamp.
	klines, err := s.Binance.GetRecentKlines(s.Cfg.Symbol, "1h", 1)
	if err != nil {
		return 0, 0, err
	}
	if len(klines) == 0 {
		return 0, 0, fmt.Errorf("no klines found")
	}

	k := klines[0]
	h, _ := strconv.ParseFloat(k.High, 64)
	l, _ := strconv.ParseFloat(k.Low, 64)

	return h, l, nil
}
