package core

import (
	"time"

	"grid-trading-btc-binance/internal/config"
	"grid-trading-btc-binance/internal/logger"
	"grid-trading-btc-binance/internal/metrics"
	"grid-trading-btc-binance/internal/repository"
	"grid-trading-btc-binance/internal/service"
)

type Bot struct {
	Cfg               *config.Config
	Metrics           *metrics.Tracker
	BalanceRepo       *repository.BalanceRepository
	TransactionRepo   *repository.TransactionRepository
	MarketDataService *service.MarketDataService
	Strategy          *Strategy
	DataCollector     *service.DataCollector

	lastBNBPrice float64
}

func NewBot(cfg *config.Config, balanceRepo *repository.BalanceRepository, transactionRepo *repository.TransactionRepository, marketDataService *service.MarketDataService, strategy *Strategy, dataCollector *service.DataCollector) *Bot {
	return &Bot{
		Cfg:               cfg,
		Metrics:           metrics.NewTracker(cfg),
		BalanceRepo:       balanceRepo,
		TransactionRepo:   transactionRepo,
		MarketDataService: marketDataService,
		Strategy:          strategy,
		DataCollector:     dataCollector,
		lastBNBPrice:      640.00, // Default fallback
	}
}

func (b *Bot) Run() {
	logger.Info("Starting Bot loop", "symbol", b.Cfg.Symbol)

	// Startup Analysis (User Request)
	b.Strategy.AnalyzeStartupState()

	// Start monitoring tickers
	b.MarketDataService.Start([]string{"BTCUSDT", "BNBUSDT"})

	updates := b.MarketDataService.GetUpdates()

	// Hourly Ticker for Data Collection
	// Align to next full hour
	now := time.Now()
	nextHour := now.Truncate(time.Hour).Add(time.Hour)
	delay := time.Until(nextHour)

	logger.Info("Scheduling Data Collector", "next_run", nextHour.Format(time.TimeOnly), "delay", delay)
	logger.Info("📊 CSV Generation will occur at", "time", nextHour.Format("15:04:05"))

	// Create a channel that will receive ticks starting from next hour
	dataTickerCh := make(chan time.Time)

	// Timer to wait for the first hour
	time.AfterFunc(delay, func() {
		// Trigger the first run immediately at the hour
		dataTickerCh <- time.Now()

		// Start the periodic ticker
		ticker := time.NewTicker(1 * time.Hour)
		go func() {
			for t := range ticker.C {
				dataTickerCh <- t
			}
		}()
	})

	for {
		select {
		case ticker := <-updates:
			start := time.Now()
			logger.Info("Received price update", "symbol", ticker.Symbol, "price", ticker.Price)

			if ticker.Symbol == "BNBUSDT" {
				b.lastBNBPrice = ticker.Price
			} else if ticker.Symbol == b.Cfg.Symbol {
				// Execute Strategy
				b.Strategy.Execute(ticker, b.lastBNBPrice)
			}

			// Track cycle metrics
			b.Metrics.TrackCycle(time.Since(start))

		case <-dataTickerCh:
			b.DataCollector.CollectAndSave()

		case <-time.After(1 * time.Minute):
			// Keep-alive or maintenance tasks
			logger.Debug("Bot heartbeat")
		}
	}
}
