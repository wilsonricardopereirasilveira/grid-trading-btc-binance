package core

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"grid-trading-btc-binance/internal/config"
	"grid-trading-btc-binance/internal/logger"
	"grid-trading-btc-binance/internal/model"
	"grid-trading-btc-binance/internal/repository"
	"grid-trading-btc-binance/internal/service"
)

type Strategy struct {
	Cfg             *config.Config
	BalanceRepo     *repository.BalanceRepository
	TransactionRepo *repository.TransactionRepository
	TelegramService *service.TelegramService
}

func NewStrategy(cfg *config.Config, balanceRepo *repository.BalanceRepository, transactionRepo *repository.TransactionRepository, telegramService *service.TelegramService) *Strategy {
	return &Strategy{
		Cfg:             cfg,
		BalanceRepo:     balanceRepo,
		TransactionRepo: transactionRepo,
		TelegramService: telegramService,
	}
}

func (s *Strategy) Execute(ticker model.Ticker, bnbPrice float64) {
	// 1. Fetch Data
	transactions := s.TransactionRepo.GetAll()

	// Filter open and filled orders
	var openOrders []model.Transaction
	var filledOrders []model.Transaction

	for _, tx := range transactions {
		if tx.Symbol == s.Cfg.Symbol && tx.Type == "buy" {
			if tx.StatusTransaction == "open" {
				openOrders = append(openOrders, tx)
			} else if tx.StatusTransaction == "filled" {
				filledOrders = append(filledOrders, tx)
			}
		}
	}

	// 2. Process Fills (Maker)
	s.processFills(openOrders, ticker.Price)

	// 3. Check Take Profit (Taker)
	// Re-fetch filled orders after potential fills
	// Optimization: We could just append to local slice if we filled something,
	// but fetching again ensures consistency if processFills modified state.
	// For simplicity in this step, we'll assume processFills might have added to filledOrders.
	// But since we are in a single-threaded loop, we can just re-read or return updated lists.
	// Let's re-read for safety.
	transactions = s.TransactionRepo.GetAll()
	filledOrders = []model.Transaction{}
	for _, tx := range transactions {
		if tx.Symbol == s.Cfg.Symbol && tx.Type == "buy" && tx.StatusTransaction == "filled" {
			filledOrders = append(filledOrders, tx)
		}
	}

	if s.checkTakeProfit(filledOrders, ticker.Price, bnbPrice) {
		return // If we sold everything, we wait for next cycle to maybe buy again
	}

	// 4. Place New Grid Orders (Maker)
	// Re-fetch open/filled to be sure
	transactions = s.TransactionRepo.GetAll()
	openOrders = []model.Transaction{}
	filledOrders = []model.Transaction{}
	for _, tx := range transactions {
		if tx.Symbol == s.Cfg.Symbol && tx.Type == "buy" {
			if tx.StatusTransaction == "open" {
				openOrders = append(openOrders, tx)
			} else if tx.StatusTransaction == "filled" {
				filledOrders = append(filledOrders, tx)
			}
		}
	}

	s.placeNewGridOrders(openOrders, filledOrders, ticker.Price, bnbPrice)
}

const (
	FeeRateBNB = 0.00075 // 0.075%
	FeeRateStd = 0.00100 // 0.10%
	BNBBuffer  = 1.1     // 10% safety buffer
)

func (s *Strategy) processFills(openOrders []model.Transaction, currentPrice float64) {
	for _, order := range openOrders {
		orderPrice, _ := strconv.ParseFloat(order.Price, 64)
		orderQty, _ := strconv.ParseFloat(order.Amount, 64)

		// Maker Fill Logic: If current price drops below or hits our buy order
		if currentPrice <= orderPrice {
			logger.Info("⚡ Maker Fill Detected", "price", orderPrice, "current", currentPrice)

			// Update Order
			order.StatusTransaction = "filled"

			// Determine Fee Source from Notes (set during creation)
			// Logic: If note contains [Fee:BTC], we deduct from asset here.
			// If [Fee:BNB], we credit full amount (BNB already deducted).
			finalQty := orderQty
			feeVal, _ := strconv.ParseFloat(order.Fee, 64)

			if strings.Contains(order.Notes, "[Fee:BTC]") {
				finalQty = orderQty - feeVal
				order.Amount = fmt.Sprintf("%.8f", finalQty) // Update stored amount
				logger.Info("📉 Deducting Asset Fee (No BNB)", "fee_btc", feeVal, "net_btc", finalQty)
			} else {
				// Assumes [Fee:BNB] or legacy
				logger.Info("💎 Fee paid in BNB (or legacy), crediting full BTC")
			}

			order.Notes += " | Maker Fill (Market Crossed)"

			// Credit BTC (Asset)
			s.updateBalance("BTC", finalQty) // Add Net BTC

			// Update in Repo
			s.TransactionRepo.Update(order)

			// Notify Telegram
			s.TelegramService.SendTradeNotification(order, 0, nil, 0, 0)
		}
	}
}

func (s *Strategy) checkTakeProfit(filledOrders []model.Transaction, currentBid, bnbPrice float64) bool {
	if len(filledOrders) == 0 {
		return false
	}

	var totalQty, totalCost float64
	var ordersToClose []model.Transaction

	for _, order := range filledOrders {
		qty, _ := strconv.ParseFloat(order.Amount, 64)
		price, _ := strconv.ParseFloat(order.Price, 64)

		// Adjust cost basis if fee was taken from asset (approximate)
		// Ideally we track net qty, but here we use raw amount from TX
		totalQty += qty
		totalCost += (qty * price)
		ordersToClose = append(ordersToClose, order)
	}

	if totalQty <= 0 {
		return false
	}

	grossValue := totalQty * currentBid

	// Est. Entry Fee (Already paid) - needed for Net Profit calc
	// We use the simpler method: Gross - Cost.
	// But strictly: Net = Gross - Cost - Fees.
	// Check existing logic: entryFee := totalCost * s.Cfg.MakerFeePct
	// We will use 0.1% as 'estimate' for profit calculation safety, or Config.
	// Let's stick to Config for 'Required Profit' check to be consistent with user setting.

	// Calculate EXIT FEE for decision
	var exitFeeVal, netUSDT float64
	var feeCurrency string
	var noteTag string

	currentBNB := s.getBalance("BNB")
	// Safety Check: Zero BNB Price
	if bnbPrice <= 0 {
		logger.Warn("⚠️ Invalid BNB Price (0 or neg), forcing Standard Fee", "bnb_price", bnbPrice)
		exitFeeVal = grossValue * FeeRateStd
		feeCurrency = "USDT"
		noteTag = "[Fee:USDT] (Fallback)"
		netUSDT = grossValue - exitFeeVal
	} else {
		feeBNB := (grossValue * FeeRateBNB) / bnbPrice

		if currentBNB >= feeBNB*BNBBuffer {
			// Pay with BNB
			exitFeeVal = feeBNB
			feeCurrency = "BNB"
			noteTag = "[Fee:BNB]"
			// Cost for net profit check (in USDT equiv)
			costOfFee := grossValue * FeeRateBNB
			netUSDT = grossValue - costOfFee
		} else {
			// Pay with USDT (Std)
			exitFeeVal = grossValue * FeeRateStd
			feeCurrency = "USDT"
			noteTag = "[Fee:USDT]"
			netUSDT = grossValue - exitFeeVal
		}
	}

	// Net Profit Calculation
	// Net = (Gross - ExitFee) - (Cost + EntryFee)
	// Entry fee is sunk cost, usually included in 'Cost' if we deducted form asset?
	// If [Fee:BTC] was used on entry, 'totalQty' is already net?
	// The system is a bit complex on partial fills.
	// Let's keep the simple Profit check: (NetValue - Cost) / Cost

	totalProfit := netUSDT - totalCost

	requiredProfit := totalCost * s.Cfg.MinNetProfitPct

	if totalProfit >= requiredProfit {
		logger.Info("💰 Take Profit Triggered", "net_profit", totalProfit, "required", requiredProfit)

		// Create Sell Transaction
		sellTx := model.Transaction{
			ID:                fmt.Sprintf("SELL_%d", time.Now().UnixMilli()),
			App:               s.Cfg.App,
			Source:            s.Cfg.Source,
			Exchange:          s.Cfg.Exchange,
			TransactionID:     fmt.Sprintf("SELL_%d", time.Now().UnixMilli()),
			Symbol:            s.Cfg.Symbol,
			Type:              "sell",
			Amount:            fmt.Sprintf("%.8f", totalQty),
			Price:             fmt.Sprintf("%.2f", currentBid),
			Fee:               fmt.Sprintf("%.8f", exitFeeVal),
			StatusTransaction: "filled",
			Notes:             fmt.Sprintf("TAKER PROFIT: $%.4f | %s", totalProfit, noteTag),
			CreatedAt:         time.Now(),
			UpdatedAt:         time.Now(),
		}
		now := time.Now()
		sellTx.ClosedAt = &now

		s.TransactionRepo.Save(sellTx)

		// Close old orders
		for _, order := range ordersToClose {
			order.StatusTransaction = "closed"
			order.Notes += " | Sold via Take Profit"
			order.ClosedAt = &now
			s.TransactionRepo.Update(order)
		}

		// Update Balances
		if feeCurrency == "BNB" {
			s.updateBalance("USDT", grossValue) // Credit Full USDT
			s.updateBalance("BNB", -exitFeeVal) // Debit BNB
		} else {
			s.updateBalance("USDT", netUSDT) // Credit Net USDT
		}
		s.updateBalance("BTC", -totalQty) // Debit BTC

		// Notify Telegram (Sell)
		finalUSDT := s.getBalance("USDT")
		finalBNB := s.getBalance("BNB")
		s.TelegramService.SendTradeNotification(sellTx, totalProfit, ordersToClose, finalUSDT, finalBNB)

		return true
	}
	return false
}

func (s *Strategy) placeNewGridOrders(openOrders, filledOrders []model.Transaction, currentAsk, bnbPrice float64) {
	allOrders := append(openOrders, filledOrders...)

	// Sort by price ascending
	sort.Slice(allOrders, func(i, j int) bool {
		p1, _ := strconv.ParseFloat(allOrders[i].Price, 64)
		p2, _ := strconv.ParseFloat(allOrders[j].Price, 64)
		return p1 < p2
	})

	lowestPrice := currentAsk
	if len(allOrders) > 0 {
		p, _ := strconv.ParseFloat(allOrders[0].Price, 64)
		lowestPrice = p
	}

	// Check drop percentage
	dropPct := (lowestPrice - currentAsk) / lowestPrice

	isFirstBuy := len(allOrders) == 0
	priceInRange := currentAsk >= s.Cfg.RangeMin && currentAsk <= s.Cfg.RangeMax

	if priceInRange && (isFirstBuy || dropPct >= s.Cfg.GridSpacingPct) {
		if len(allOrders) < s.Cfg.GridLevels {
			executionPrice := currentAsk
			currentLevel := len(allOrders) + 1

			// Calculate Order Value
			saldoUSDT := s.getBalance("USDT")
			rawOrderValue := saldoUSDT * s.Cfg.PositionSizePct
			orderValue := math.Max(rawOrderValue, 5.0) // Min $5

			if saldoUSDT >= orderValue {
				buyQty := orderValue / executionPrice

				// FEE LOGIC: BNB vs Asset
				currentBNB := s.getBalance("BNB")
				var finalFeeVal float64
				var noteTag string

				// SAFETY Check: BNB Price
				if bnbPrice <= 0 {
					noteTag = "[Fee:BTC] (NoBNBData)"
					finalFeeVal = buyQty * FeeRateStd
				} else {
					feeBNB := (orderValue * FeeRateBNB) / bnbPrice

					if currentBNB >= feeBNB*BNBBuffer {
						// Has BNB, pay with BNB
						finalFeeVal = feeBNB
						noteTag = "[Fee:BNB]"
						s.updateBalance("BNB", -feeBNB)
					} else {
						// No BNB, pay with Asset (BTC)
						noteTag = "[Fee:BTC]"
						finalFeeVal = buyQty * FeeRateStd
					}
				}

				// Create Buy Transaction
				buyTx := model.Transaction{
					ID:                fmt.Sprintf("BUY_%d_L%d", time.Now().UnixMilli(), currentLevel),
					App:               s.Cfg.App,
					Source:            s.Cfg.Source,
					Exchange:          s.Cfg.Exchange,
					TransactionID:     fmt.Sprintf("BUY_%d_L%d", time.Now().UnixMilli(), currentLevel),
					Symbol:            s.Cfg.Symbol,
					Type:              "buy",
					Amount:            fmt.Sprintf("%.8f", buyQty),
					Price:             fmt.Sprintf("%.2f", executionPrice),
					Fee:               fmt.Sprintf("%.8f", finalFeeVal),
					StatusTransaction: "open",
					Notes:             fmt.Sprintf("Grid L%d (Maker) %s", currentLevel, noteTag),
					CreatedAt:         time.Now(),
					UpdatedAt:         time.Now(),
				}

				s.TransactionRepo.Save(buyTx)

				// Update Balances (Lock funds)
				s.updateBalance("USDT", -orderValue)

				logger.Info("📌 Maker Order Placed", "level", currentLevel, "price", executionPrice, "fee_type", noteTag)
			} else {
				logger.Warn("Insufficient funds for new order", "needed", orderValue, "have", saldoUSDT)
			}
		} else {
			logger.Debug("Grid full")
		}
	}
}

func (s *Strategy) getBalance(currency string) float64 {
	b, ok := s.BalanceRepo.Get(currency)
	if !ok {
		return 0
	}
	return b.Amount
}

func (s *Strategy) updateBalance(currency string, amount float64) {
	current := s.getBalance(currency)
	s.BalanceRepo.Update(currency, current+amount)
}

func (s *Strategy) AnalyzeStartupState() {
	logger.Info("🔄 Analyzing Startup State from transactions.json...")

	transactions := s.TransactionRepo.GetAll()
	var openBuyCount int
	var filledInventoryCount int
	var totalInventoryBTC float64
	var lowestPrice float64 = 99999999.0
	var highestPrice float64 = 0.0

	for _, tx := range transactions {
		if tx.Symbol == s.Cfg.Symbol && tx.Type == "buy" {
			if tx.StatusTransaction == "open" {
				openBuyCount++
				price, _ := strconv.ParseFloat(tx.Price, 64)
				if price < lowestPrice {
					lowestPrice = price
				}
				if price > highestPrice {
					highestPrice = price
				}
			} else if tx.StatusTransaction == "filled" {
				filledInventoryCount++
				qty, _ := strconv.ParseFloat(tx.Amount, 64)
				totalInventoryBTC += qty
			}
		}
	}

	if openBuyCount == 0 {
		lowestPrice = 0
	}

	logger.Info("📊 Startup Summary",
		"open_grid_orders", openBuyCount,
		"filled_inventory_orders", filledInventoryCount,
		"total_btc_held", fmt.Sprintf("%.8f", totalInventoryBTC),
		"grid_range_min", lowestPrice,
		"grid_range_max", highestPrice,
	)

	if filledInventoryCount > 0 {
		logger.Info("⚠️ Inventory detected on startup. Bot will check Take Profit conditions immediately on first price tick.")
	} else {
		logger.Info("✅ No inventory. Bot starts clean/neutral.")
	}
}
