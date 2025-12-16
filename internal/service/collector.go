package service

import (
	"encoding/csv"
	"fmt"
	"os"
	"strconv"
	"time"

	"grid-trading-btc-binance/internal/config"
	"grid-trading-btc-binance/internal/logger"
	"grid-trading-btc-binance/internal/repository"
)

type DataCollector struct {
	Cfg             *config.Config
	BalanceRepo     *repository.BalanceRepository
	TransactionRepo *repository.TransactionRepository
	MarketData      *MarketDataService
}

func NewDataCollector(cfg *config.Config, balanceRepo *repository.BalanceRepository, transactionRepo *repository.TransactionRepository, marketData *MarketDataService) *DataCollector {
	return &DataCollector{
		Cfg:             cfg,
		BalanceRepo:     balanceRepo,
		TransactionRepo: transactionRepo,
		MarketData:      marketData,
	}
}

func (c *DataCollector) CollectAndSave() {
	logger.Info("📊 Collecting hourly data...")

	// 1. Prepare Data
	now := time.Now()
	timestamp := now.Format(time.RFC3339)

	// Market Data
	btcPrice, _ := c.MarketData.GetPrice("BTCUSDT")
	bnbPrice, _ := c.MarketData.GetPrice("BNBUSDT")
	inRange := "false"
	if btcPrice >= c.Cfg.RangeMin && btcPrice <= c.Cfg.RangeMax {
		inRange = "true"
	}

	// Range Utilization
	rangeDiff := c.Cfg.RangeMax - c.Cfg.RangeMin
	rangeUtilizationPct := 0.0
	if rangeDiff > 0 {
		rangeUtilizationPct = ((btcPrice - c.Cfg.RangeMin) / rangeDiff) * 100
	}

	// Wallet Data
	balanceUSDT := c.getBalance("USDT")
	balanceBTC := c.getBalance("BTC")
	balanceBNB := c.getBalance("BNB")

	// Strategy Equity (USDT + BTC Value)
	strategyEquity := balanceUSDT + (balanceBTC * btcPrice)

	// Inventory Ratio
	// Ratio = (BTC Value) / Total Equity
	inventoryRatio := 0.0
	if strategyEquity > 0 {
		inventoryRatio = (balanceBTC * btcPrice) / strategyEquity
	}

	// Open Orders & Position Analysis (Unrealized PnL)
	allTx := c.TransactionRepo.GetAll()
	openOrdersCount := 0
	totalCostBasis := 0.0
	totalQtyFilled := 0.0

	for _, tx := range allTx {
		if tx.Symbol == c.Cfg.Symbol {
			if tx.StatusTransaction == "open" {
				openOrdersCount++
			} else if tx.StatusTransaction == "filled" && tx.Type == "buy" {
				p, _ := strconv.ParseFloat(tx.Price, 64)
				q, _ := strconv.ParseFloat(tx.Amount, 64)
				totalCostBasis += (p * q)
				totalQtyFilled += q
			}
		}
	}

	avgEntryPrice := 0.0
	if totalQtyFilled > 0 {
		avgEntryPrice = totalCostBasis / totalQtyFilled
	}

	unrealizedPnL := 0.0
	if balanceBTC > 0 && avgEntryPrice > 0 {
		unrealizedPnL = (btcPrice - avgEntryPrice) * balanceBTC
	}

	// Performance Hourly (Last 1h)
	lastHour := now.Add(-1 * time.Hour)
	recentTx := c.TransactionRepo.GetTransactionsAfter(lastHour)

	tradesTotal := 0
	tradesBuy := 0
	tradesSell := 0
	volumeUSDT := 0.0
	volumeBTC := 0.0
	realizedProfit := 0.0
	totalBuyPrice := 0.0
	totalSellPrice := 0.0
	feesBNB := 0.0

	for _, tx := range recentTx {
		tradesTotal++
		amount, _ := strconv.ParseFloat(tx.Amount, 64)
		price, _ := strconv.ParseFloat(tx.Price, 64)
		fee, _ := strconv.ParseFloat(tx.Fee, 64)
		totalVal := amount * price

		// Accumulate Fees (Assuming BNB per user request)
		feesBNB += fee

		if tx.Type == "buy" {
			tradesBuy++
			totalBuyPrice += price
		} else if tx.Type == "sell" {
			tradesSell++
			volumeUSDT += totalVal
			volumeBTC += amount
			totalSellPrice += price

			// Extract profit from notes if possible, or calculate rough approx?
			// The user asked for "realized_profit_usdt".
			// In our Strategy, we put "TAKER PROFIT: $0.1234" in Notes.
			// Let's try to parse it or just leave 0 for now if too complex.
			// Simple parsing:
			var profit float64
			fmt.Sscanf(tx.Notes, "TAKER PROFIT: $%f", &profit)
			realizedProfit += profit
		}
	}

	avgBuyPrice := 0.0
	if tradesBuy > 0 {
		avgBuyPrice = totalBuyPrice / float64(tradesBuy)
	}

	avgSellPrice := 0.0
	if tradesSell > 0 {
		avgSellPrice = totalSellPrice / float64(tradesSell)
	}

	feesUSDTEquiv := feesBNB * bnbPrice

	// 2. Prepare CSV Record
	record := []string{
		timestamp,
		"grid-trading-btc-binance", // strategy_name
		"binance",                  // exchange (hardcoded as strategy is binance specific)
		c.Cfg.Symbol,
		"1h", // timeframe

		// Config
		fmt.Sprintf("%d", c.Cfg.GridLevels),
		fmt.Sprintf("%.2f", c.Cfg.RangeMin),
		fmt.Sprintf("%.2f", c.Cfg.RangeMax),
		fmt.Sprintf("%.4f", c.Cfg.PositionSizePct),
		fmt.Sprintf("%.4f", c.Cfg.StopLossPct),

		// Market
		fmt.Sprintf("%.2f", btcPrice),
		fmt.Sprintf("%.2f", bnbPrice),
		inRange,

		// Wallet
		fmt.Sprintf("%.2f", balanceUSDT),
		fmt.Sprintf("%.8f", balanceBTC),
		fmt.Sprintf("%.8f", balanceBNB),
		fmt.Sprintf("%.2f", strategyEquity),
		fmt.Sprintf("%.4f", inventoryRatio),

		// Performance
		fmt.Sprintf("%d", tradesTotal),
		fmt.Sprintf("%d", tradesBuy),
		fmt.Sprintf("%d", tradesSell),
		fmt.Sprintf("%.2f", volumeUSDT),
		fmt.Sprintf("%.8f", volumeBTC),
		fmt.Sprintf("%.4f", realizedProfit),
		fmt.Sprintf("%.2f", avgBuyPrice),
		fmt.Sprintf("%.2f", avgSellPrice),
		fmt.Sprintf("%.8f", feesBNB),
		fmt.Sprintf("%.4f", feesUSDTEquiv),
		fmt.Sprintf("%d", openOrdersCount),
		fmt.Sprintf("%.8f", feesBNB),
		fmt.Sprintf("%.4f", feesUSDTEquiv),
		fmt.Sprintf("%d", openOrdersCount),
		fmt.Sprintf("%.4f", unrealizedPnL),
		fmt.Sprintf("%.2f", rangeUtilizationPct),
	}

	// 3. Save to CSV
	// Ensure folder exists
	if _, err := os.Stat("logs"); os.IsNotExist(err) {
		os.Mkdir("logs", 0755)
	}
	c.appendToCSV("logs/analyze_strategy.csv", record)
}

func (c *DataCollector) getBalance(currency string) float64 {
	b, ok := c.BalanceRepo.Get(currency)
	if !ok {
		return 0
	}
	return b.Amount
}

func (c *DataCollector) appendToCSV(filename string, record []string) {
	fileExists := false
	if _, err := os.Stat(filename); err == nil {
		fileExists = true
	}

	f, err := os.OpenFile(filename, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		logger.Error("Failed to open CSV", "error", err)
		return
	}
	defer f.Close()

	w := csv.NewWriter(f)
	defer w.Flush()

	// Write Header if new file
	if !fileExists {
		header := []string{
			"timestamp", "strategy_name", "exchange", "symbol", "timeframe",
			"grid_levels", "range_min", "range_max", "position_size_pct", "stop_loss_pct",
			"btc_price", "bnb_price", "in_range",
			"balance_usdt", "balance_btc", "balance_bnb", "strategy_equity_usdt", "inventory_ratio_btc",
			"trades_total", "trades_buy", "trades_sell", "volume_usdt", "volume_btc", "realized_profit_usdt", "avg_buy_price", "avg_sell_price",
			"total_fees_bnb", "total_fees_usdt_equiv", "open_orders_count", "unrealized_pnl_usdt", "range_utilization_pct",
		}
		if err := w.Write(header); err != nil {
			logger.Error("Failed to write CSV header", "error", err)
		}
	}

	if err := w.Write(record); err != nil {
		logger.Error("Failed to write CSV record", "error", err)
	}
}
