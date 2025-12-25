package main

import (
	"fmt"
	"log"
	"strconv"
	"time"

	"grid-trading-btc-binance/internal/api"
	"grid-trading-btc-binance/internal/config"
	"grid-trading-btc-binance/internal/core"
	"grid-trading-btc-binance/internal/logger"
	"grid-trading-btc-binance/internal/market"
	"grid-trading-btc-binance/internal/model"
	"grid-trading-btc-binance/internal/repository"
	"grid-trading-btc-binance/internal/service"
)

func main() {
	logger.Init()
	logger.Info("Starting Grid Trading Strategy (Production Mode)...")

	cfg, err := config.Load()
	if err != nil {
		log.Fatalf("Failed to load configuration: %v", err)
	}

	logger.Info("Configuration loaded successfully",
		"symbol", cfg.Symbol,
		"grid_levels", cfg.GridLevels,
		"range_min", cfg.RangeMin,
		"range_max", cfg.RangeMax,
		"taker_fee", cfg.TakerFeePct,
		"maker_fee", cfg.MakerFeePct,
		"high_vol_mult", cfg.HighVolMultiplier,
		"low_vol_mult", cfg.LowVolMultiplier,
	)

	// Initialize Repositories
	storage := repository.NewStorage()
	balanceRepo := repository.NewBalanceRepository()
	transactionRepo := repository.NewTransactionRepository(storage)

	// Initialize Binance API Client
	binanceClient := api.NewBinanceClient(cfg.BinanceApiKey, cfg.BinanceSecretKey)
	if err := binanceClient.SyncTime(); err != nil {
		logger.Warn("⚠️ Failed to synchronize time with Binance, using local time", "error", err)
	}

	// Fetch Initial Balance & Fees
	accountInfo, err := binanceClient.GetAccountInfo()
	if err != nil {
		logger.Error("Failed to fetch initial account info from Binance", "error", err)
	} else {
		// Sync Balances
		syncBalances(balanceRepo, accountInfo)

		// Sync Fees
		syncFees(cfg, accountInfo)
		logger.Info("Initial account info synchronized from Binance")
	}

	// Start Periodic Balance & Fee Sync (1 minute)
	go func() {
		ticker := time.NewTicker(1 * time.Minute)
		defer ticker.Stop()
		for range ticker.C {
			info, err := binanceClient.GetAccountInfo()
			if err != nil {
				logger.Error("Failed to sync account info from Binance", "error", err)
				continue
			}
			syncBalances(balanceRepo, info)
			syncFees(cfg, info)
			logger.Info("Account info synchronized from Binance (1m check)")
		}
	}()

	if err := transactionRepo.Load(); err != nil {
		logger.Error("Failed to load transactions", "error", err)
	}

	// Services
	// Services
	marketDataService := service.NewMarketDataService()
	volatilityService := market.NewVolatilityService(cfg, binanceClient)
	dataCollector := service.NewDataCollector(cfg, balanceRepo, transactionRepo, marketDataService, volatilityService)
	telegramService := service.NewTelegramService(cfg)
	streamService := service.NewStreamService(binanceClient)

	// Start Volatility Polling
	volatilityService.StartPolling()

	// Strategy
	strategy := core.NewStrategy(cfg, balanceRepo, transactionRepo, telegramService, binanceClient, volatilityService)

	// Bot
	bot := core.NewBot(cfg, balanceRepo, transactionRepo, marketDataService, strategy, dataCollector)

	// Analyze Startup State
	strategy.AnalyzeStartupState()

	// Sync Orders with Binance (Handle Offline Changes)
	strategy.SyncOrdersOnStartup()

	// Start Periodic Order Sync (Every 5 min)
	strategy.StartPeriodicSync()

	// Start WebSocket Stream
	go func() {
		// Simple retry loop for stream start
		for {
			if err := streamService.Start(); err != nil {
				logger.Error("❌ Failed to start WebSocket Stream, retrying in 10s...", "error", err)
				time.Sleep(10 * time.Second)
				continue
			}
			// Blocked inside Start() -> readLoop
			// If it returns, it disconnected
			logger.Warn("⚠️ WebSocket Stream disconnected, reconnecting in 5s...")
			time.Sleep(5 * time.Second)
		}
	}()

	// Listen for WebSocket Updates
	go func() {
		for update := range streamService.Updates {
			strategy.HandleOrderUpdate(update)
		}
	}()

	bot.Run()
}

func syncBalances(repo *repository.BalanceRepository, info *api.AccountInfoResponse) {
	var balances []model.Balance
	for _, b := range info.Balances {
		free, _ := strconv.ParseFloat(b.Free, 64)
		locked, _ := strconv.ParseFloat(b.Locked, 64)

		if free > 0 || locked > 0 {
			balances = append(balances, model.Balance{
				Currency: b.Asset,
				Amount:   free, // Using Free balance for trading availability
			})
		}
	}
	repo.SetBalances(balances)
}

func syncFees(cfg *config.Config, info *api.AccountInfoResponse) {
	// Binance fees are in basis points (commission rate * 10000)
	// Example: 10 => 0.0010 (0.10%)
	makerFee := float64(info.MakerCommission) / 10000.0
	takerFee := float64(info.TakerCommission) / 10000.0

	updated := false

	if makerFee != cfg.MakerFeePct {
		logger.Info("🔄 Maker Fee Updated from API", "old", cfg.MakerFeePct, "new", makerFee)
		cfg.MakerFeePct = makerFee
		if err := config.UpdateEnvVariable("MAKER_FEE_PCT", fmt.Sprintf("%.6f", makerFee)); err != nil {
			logger.Error("Failed to update .env for MAKER_FEE_PCT", "error", err)
		}
		updated = true
	}

	if takerFee != cfg.TakerFeePct {
		logger.Info("🔄 Taker Fee Updated from API", "old", cfg.TakerFeePct, "new", takerFee)
		cfg.TakerFeePct = takerFee
		if err := config.UpdateEnvVariable("TAKER_FEE_PCT", fmt.Sprintf("%.6f", takerFee)); err != nil {
			logger.Error("Failed to update .env for TAKER_FEE_PCT", "error", err)
		}
		updated = true
	}

	if updated {
		logger.Info("✅ Fees synchronized with Binance and .env updated")
	}
}
