package main

import (
	"log"

	"grid-trading-btc-binance/internal/config"
	"grid-trading-btc-binance/internal/core"
	"grid-trading-btc-binance/internal/logger"
	"grid-trading-btc-binance/internal/repository"
	"grid-trading-btc-binance/internal/service"
)

func main() {
	logger.Init()
	logger.Info("Starting Grid Trading Strategy (Paper Trading)...")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	logger.Info("Configuration loaded successfully",
		"symbol", cfg.Symbol,
		"exchange", cfg.Exchange,
		"grid_levels", cfg.GridLevels,
		"range_min", cfg.RangeMin,
		"range_max", cfg.RangeMax,
	)

	// Initialize Repositories
	storage := repository.NewStorage()
	balanceRepo := repository.NewBalanceRepository(storage)
	transactionRepo := repository.NewTransactionRepository(storage)

	if err := balanceRepo.Load(); err != nil {
		logger.Error("Failed to load balances", "error", err)
	}

	if err := transactionRepo.Load(); err != nil {
		logger.Error("Failed to load transactions", "error", err)
	}

	// Services
	marketDataService := service.NewMarketDataService()
	dataCollector := service.NewDataCollector(cfg, balanceRepo, transactionRepo, marketDataService)
	telegramService := service.NewTelegramService(cfg)

	// Strategy
	strategy := core.NewStrategy(cfg, balanceRepo, transactionRepo, telegramService)

	// Bot
	bot := core.NewBot(cfg, balanceRepo, transactionRepo, marketDataService, strategy, dataCollector)
	bot.Run()
}
