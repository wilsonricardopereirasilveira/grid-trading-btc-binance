package config

import (
	"fmt"
	"os"
	"strconv"

	"github.com/joho/godotenv"
)

type Config struct {
	Symbol          string
	MakerFeePct     float64
	TakerFeePct     float64
	GridLevels      int
	GridSpacingPct  float64
	PositionSizePct float64
	MinNetProfitPct float64
	StopLossPct     float64
	MaxSpreadPct    float64
	RangeMin        float64
	RangeMax        float64
	MinOrderValue   float64

	// Smart Entry Repositioning
	SmartEntryRepositionPct      float64
	SmartEntryRepositionCooldown int

	// Metrics
	MsTimeProduction int64
	TotalCycles      int64

	// Binance API
	BinanceApiKey    string
	BinanceSecretKey string

	// Telegram
	TelegramToken  string
	TelegramChatID string
}

func Load() (*Config, error) {
	if err := godotenv.Load(); err != nil {
		return nil, fmt.Errorf("error loading .env file: %w", err)
	}

	cfg := &Config{}
	var err error

	cfg.Symbol = os.Getenv("SYMBOL")
	if cfg.Symbol == "" {
		return nil, fmt.Errorf("SYMBOL is required")
	}

	cfg.MakerFeePct, err = parseFloat(os.Getenv("MAKER_FEE_PCT"), "MAKER_FEE_PCT")
	if err != nil {
		return nil, err
	}

	cfg.TakerFeePct, err = parseFloat(os.Getenv("TAKER_FEE_PCT"), "TAKER_FEE_PCT")
	if err != nil {
		return nil, err
	}

	cfg.GridLevels, err = parseInt(os.Getenv("GRID_LEVELS"), "GRID_LEVELS")
	if err != nil {
		return nil, err
	}

	cfg.GridSpacingPct, err = parseFloat(os.Getenv("GRID_SPACING_PCT"), "GRID_SPACING_PCT")
	if err != nil {
		return nil, err
	}

	cfg.PositionSizePct, err = parseFloat(os.Getenv("POSITION_SIZE_PCT"), "POSITION_SIZE_PCT")
	if err != nil {
		return nil, err
	}

	cfg.MinNetProfitPct, err = parseFloat(os.Getenv("MIN_NET_PROFIT_PCT"), "MIN_NET_PROFIT_PCT")
	if err != nil {
		return nil, err
	}

	cfg.StopLossPct, err = parseFloat(os.Getenv("STOP_LOSS_PCT"), "STOP_LOSS_PCT")
	if err != nil {
		return nil, err
	}

	cfg.MaxSpreadPct, err = parseFloat(os.Getenv("MAX_SPREAD_PCT"), "MAX_SPREAD_PCT")
	if err != nil {
		return nil, err
	}

	cfg.RangeMin, err = parseFloat(os.Getenv("RANGE_MIN"), "RANGE_MIN")
	if err != nil {
		return nil, err
	}

	cfg.RangeMax, err = parseFloat(os.Getenv("RANGE_MAX"), "RANGE_MAX")
	if err != nil {
		return nil, err
	}

	cfg.MinOrderValue, err = parseFloat(os.Getenv("MIN_ORDER_VALUE"), "MIN_ORDER_VALUE")
	if err != nil {
		return nil, err
	}

	// Smart Entry Defaults (Optional params)
	valRepositionPct := os.Getenv("SMART_ENTRY_REPOSITION_PCT")
	if valRepositionPct != "" {
		cfg.SmartEntryRepositionPct, err = parseFloat(valRepositionPct, "SMART_ENTRY_REPOSITION_PCT")
		if err != nil {
			return nil, err
		}
	} else {
		cfg.SmartEntryRepositionPct = 0.005
	}

	valCooldown := os.Getenv("SMART_ENTRY_REPOSITION_COOLDOWN_MIN")
	if valCooldown != "" {
		cfg.SmartEntryRepositionCooldown, err = parseInt(valCooldown, "SMART_ENTRY_REPOSITION_COOLDOWN_MIN")
		if err != nil {
			return nil, err
		}
	} else {
		cfg.SmartEntryRepositionCooldown = 5
	}

	// We no longer load metrics from .env, but we keep the struct fields for runtime usage if needed.
	// Actually, user said to remove from .env but keep showing in log.
	// We can initialize them to 0 or defaults here if we want, or just leave them as 0.
	// The requirement: "não popule nada no .env".
	// So we don't read them from .env.

	cfg.BinanceApiKey = os.Getenv("BINANCE_API_KEY")
	cfg.BinanceSecretKey = os.Getenv("BINANCE_SECRET_KEY")

	cfg.TelegramToken = os.Getenv("TELEGRAM_TOKEN")
	cfg.TelegramChatID = os.Getenv("TELEGRAM_CHAT_ID")

	return cfg, nil
}

func UpdateEnvVariable(key, value string) error {
	envMap, err := godotenv.Read()
	if err != nil {
		return fmt.Errorf("error reading .env file: %w", err)
	}

	envMap[key] = value

	if err := godotenv.Write(envMap, ".env"); err != nil {
		return fmt.Errorf("error writing .env file: %w", err)
	}
	return nil
}

func parseFloat(value, name string) (float64, error) {
	if value == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	f, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid value for %s: %w", name, err)
	}
	return f, nil
}

func parseInt(value, name string) (int, error) {
	if value == "" {
		return 0, fmt.Errorf("%s is required", name)
	}
	i, err := strconv.Atoi(value)
	if err != nil {
		return 0, fmt.Errorf("invalid value for %s: %w", name, err)
	}
	return i, nil
}
