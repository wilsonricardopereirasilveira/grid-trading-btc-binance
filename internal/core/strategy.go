package core

import (
	"fmt"
	"sort"
	"strconv"
	"time"

	"grid-trading-btc-binance/internal/api"
	"grid-trading-btc-binance/internal/config"
	"grid-trading-btc-binance/internal/logger"
	"grid-trading-btc-binance/internal/model"
	"grid-trading-btc-binance/internal/repository"
	"grid-trading-btc-binance/internal/service"
)

type Strategy struct {
	Cfg               *config.Config
	BalanceRepo       *repository.BalanceRepository
	TransactionRepo   *repository.TransactionRepository
	TelegramService   *service.TelegramService
	Binance           *api.BinanceClient
	lastFillCheck     time.Time
	lastUSDTAlertTime time.Time
	lastBNBAlertTime  time.Time
}

func NewStrategy(cfg *config.Config, balanceRepo *repository.BalanceRepository, transactionRepo *repository.TransactionRepository, telegramService *service.TelegramService, binanceClient *api.BinanceClient) *Strategy {
	return &Strategy{
		Cfg:             cfg,
		BalanceRepo:     balanceRepo,
		TransactionRepo: transactionRepo,
		TelegramService: telegramService,
		Binance:         binanceClient,
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

	// 2. Process Fills (REMOVED - Now handled by WebSocket)
	// s.processFills(openOrders, ticker.Price)

	// 3. Check Take Profit (Taker)
	// We check this every cycle still, to catch things if WS notified us already
	// or if we rely on loop for TP check.

	// Re-fetch filled orders after potential fills
	transactions = s.TransactionRepo.GetAll()
	filledOrders = []model.Transaction{}
	activeOpenOrders := []model.Transaction{}

	for _, tx := range transactions {
		if tx.Symbol == s.Cfg.Symbol && tx.Type == "buy" {
			if tx.StatusTransaction == "filled" {
				filledOrders = append(filledOrders, tx)
			} else if tx.StatusTransaction == "open" {
				activeOpenOrders = append(activeOpenOrders, tx)
			}
		}
	}

	if s.checkTakeProfit(filledOrders, activeOpenOrders, ticker.Price, bnbPrice) {
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
	s.checkLowBNB(bnbPrice)
}

// HandleOrderUpdate processes executionReport events from WebSocket
func (s *Strategy) HandleOrderUpdate(event service.OrderUpdate) {
	if event.Symbol != s.Cfg.Symbol {
		return
	}

	logger.Info("⚡ Order Update Received",
		"id", event.ClientOrderID,
		"status", event.Status,
		"execType", event.ExecutionType,
	)

	// Fetch transaction from Repo
	tx, exists := s.TransactionRepo.Get(event.ClientOrderID)
	if !exists {
		// Possibly a manual order or one we don't track?
		// Or maybe ClientOrderID mismatch.
		// If it's a new fill for a Limit Buy we placed, we should have it in Repo.
		logger.Debug("Received update for unknown order", "id", event.ClientOrderID)
		return
	}

	if event.Status == "FILLED" {
		if tx.StatusTransaction != "filled" {
			logger.Info("⚡ WebSocket: Maker Order FILLED", "orderID", tx.ID, "price", event.LastExecPrice)

			tx.StatusTransaction = "filled"
			// Update details from event
			tx.Price = event.LastExecPrice
			// We might want avg price if multiple fills, but event usually has Last.
			// Ideally we use CumExecQty / CumQuoteQty if available or just last.
			// Event has 'z' (CumExecQty) and 'L' (LastPrice).
			// If fully filled, we can use average.
			// Calculate Avg Price = CumQuote / CumQty ?
			// Not easily available in simple event structure without 'Z' (CumQuote).
			// Let's use LastExecPrice for now or keep original if close.

			tx.Notes += " | WS Verified Fill"
			s.TransactionRepo.Update(tx)

			// Fetch fresh balances for notification as requested
			var usdtBal, bnbBal, btcBal float64
			accInfo, err := s.Binance.GetAccountInfo()
			if err != nil {
				logger.Error("⚠️ Failed to fetch fresh balances for notification", "error", err)
				// Fallback to local cache if API fails
				usdtBal = s.getBalance("USDT")
				bnbBal = s.getBalance("BNB")
				btcBal = s.getBalance("BTC")
			} else {
				// Parse balances
				for _, b := range accInfo.Balances {
					if b.Asset == "USDT" {
						usdtBal, _ = strconv.ParseFloat(b.Free, 64)
					} else if b.Asset == "BNB" {
						bnbBal, _ = strconv.ParseFloat(b.Free, 64)
					} else if b.Asset == "BTC" {
						btcBal, _ = strconv.ParseFloat(b.Free, 64)
					}
				}
				// Optional: Update local repo with fresh data while we are at it?
				// s.BalanceRepo.Update("USDT", usdtBal) ...
			}

			s.TelegramService.SendTradeNotification(tx, 0, nil, usdtBal, bnbBal, btcBal)
		}
	} else if event.Status == "CANCELED" || event.Status == "REJECTED" || event.Status == "EXPIRED" {
		if tx.StatusTransaction != "closed" {
			logger.Warn("⚠️ WebSocket: Order Closed/Canceled", "orderID", tx.ID, "status", event.Status)
			tx.StatusTransaction = "closed"
			tx.Notes += fmt.Sprintf(" | Closed via WS: %s", event.Status)
			s.TransactionRepo.Update(tx)
		}
	}
}

const (
	FeeRateBNB = 0.00075 // 0.075%
	FeeRateStd = 0.00100 // 0.10%
	BNBBuffer  = 1.1     // 10% safety buffer
)

// processFills - DEPRECATED/REMOVED (Using WebSocket)
func (s *Strategy) processFills(openOrders []model.Transaction, currentPrice float64) {
	// Intentionally Left Empty or Removed
	// We rely on HandleOrderUpdate from WebSocket now.
}

func (s *Strategy) checkTakeProfit(filledOrders, openOrders []model.Transaction, currentBid, bnbPrice float64) bool {
	if len(filledOrders) == 0 {
		return false
	}

	var totalQty, totalCost float64
	var ordersToClose []model.Transaction

	for _, order := range filledOrders {
		qty, _ := strconv.ParseFloat(order.Amount, 64)
		price, _ := strconv.ParseFloat(order.Price, 64)

		totalQty += qty
		totalCost += (qty * price)
		ordersToClose = append(ordersToClose, order)
	}

	if totalQty <= 0 {
		return false
	}

	// Calculate Profit Potential similar to before
	// ... (profit logic same) ...
	grossValue := totalQty * currentBid

	// Simplify logic for decision: Just check if Total Profit > Required
	// Fees: Taker Fee (0.1% or similar).
	// We need to estimate fee to know if it's profitable.
	// We will assume standard fee for calculation check.
	estExitFee := grossValue * s.Cfg.TakerFeePct
	netUSDT := grossValue - estExitFee
	totalProfit := netUSDT - totalCost
	requiredProfit := totalCost * s.Cfg.MinNetProfitPct

	if totalProfit >= requiredProfit {
		logger.Info("💰 Take Profit Cond Met", "net_profit", totalProfit, "required", requiredProfit)

		// 1. Create Sell Order on Binance
		// We sell the total accumulated quantity.
		side := "SELL"
		qtyStr := fmt.Sprintf("%.8f", totalQty)

		req := api.OrderRequest{
			Symbol:           s.Cfg.Symbol,
			Side:             side,
			Type:             "MARKET", // Taker execution for immediate exit
			Quantity:         qtyStr,
			NewClientOrderID: fmt.Sprintf("SELL_%d", time.Now().UnixMilli()),
		}

		resp, err := s.Binance.CreateOrder(req)
		if err != nil {
			logger.Error("❌ Failed to create Sell Order", "error", err)
			return false
		}

		logger.Info("✅ Sell Order Executed", "orderID", resp.OrderId, "filledQty", resp.ExecutedQty)

		// 2. Clear Makers from Transactions (Hybrid Model)
		// Zombie Order Management: Cancel all Open Orders first
		for _, oOrder := range openOrders {
			// Cancel order on Binance
			logger.Info("🧹 Canceling Zombie Order", "orderID", oOrder.ID, "price", oOrder.Price)
			_, err := s.Binance.CancelOrder(s.Cfg.Symbol, oOrder.ID)
			if err != nil {
				// We log error but continue to clear.
				// Often error is "Unknown Order" if it was already filled/canceled.
				logger.Warn("⚠️ Failed to cancel order (Zombie)", "orderID", oOrder.ID, "error", err)
			} else {
				logger.Info("✅ Zombie Order Cancelled", "orderID", oOrder.ID)
			}
		}

		// "removemos todas as makers que fazem parte da que agrediram a taker"
		// "processo esta completo... começamos um novo"
		// This implies the current cycle is closed.
		if err := s.TransactionRepo.Clear(); err != nil {
			logger.Error("Failed to clear transactions", "error", err)
		}

		// Notify Telegram (we can construct a 'fake' sellTx for notification or use real data)
		// We don't save the Sell TX to repo anymore as per user request ("não faz mais sentindo pra gente... excluimos tudo")
		// But we still notify.

		sellTx := model.Transaction{
			ID:                resp.ClientOrderId, // Use actual ID
			Symbol:            s.Cfg.Symbol,
			Type:              "sell",
			Amount:            resp.ExecutedQty,
			Price:             fmt.Sprintf("%.2f", currentBid), // Use bid or actual fill price from resp
			StatusTransaction: "filled",
			Notes:             fmt.Sprintf("TAKER PROFIT: $%.4f", totalProfit),
			CreatedAt:         time.Now(),
		}

		// Fill details from response
		var totalComm float64
		// Calculate average price from fills
		var totalVal float64
		var totalFilledQty float64
		for _, fill := range resp.Fills {
			p, _ := strconv.ParseFloat(fill.Price, 64)
			q, _ := strconv.ParseFloat(fill.Qty, 64)
			c, _ := strconv.ParseFloat(fill.Commission, 64) // Assuming USDT commission
			totalVal += p * q
			totalFilledQty += q
			totalComm += c
		}
		if totalFilledQty > 0 {
			avgPrice := totalVal / totalFilledQty
			sellTx.Price = fmt.Sprintf("%.2f", avgPrice)
		}
		sellTx.Fee = fmt.Sprintf("%.8f", totalComm)

		// Notify Telegram
		finalUSDT := s.getBalance("USDT") // This might be stale until next sync, but okay.
		finalBNB := s.getBalance("BNB")
		finalBTC := s.getBalance("BTC")
		s.TelegramService.SendTradeNotification(sellTx, totalProfit, ordersToClose, finalUSDT, finalBNB, finalBTC)

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
			// Calculate Order Value
			saldoUSDT := s.getBalance("USDT")
			orderValue := s.calculateOrderValue(saldoUSDT)

			if saldoUSDT >= orderValue {
				// Calculate Qty base on Price
				// For Limit order, we use 'executionPrice'. Assuming we want to buy NOW at market basically?
				// Grid usually places Limit orders below market.
				// But code says "executionPrice := currentAsk" and "dropPct >= s.Cfg.GridSpacingPct" implies we buy AS IT DROPS.
				// So we are placing a "Market" buy essentially, or a Limit at Current Ask (taker).
				// The original code used 'currentAsk' as Price.
				// For safety, let's use Limit at slightly higher or Market?
				// If we use Limit at current Ask, we might miss if moves up.
				// User strategy seems to be "Buy the dip" via immediate orders when price trigger is hit.
				// Let's use LIMIT GTC at currentAsk.

				buyQty := orderValue / executionPrice

				// 1. Create Buy Order (Maker/Position Entry) on Binance
				qtyStr := fmt.Sprintf("%.5f", buyQty) // Adjust precision! BTC usually 5 or 6?
				// Important: LotSize filter. BTCUSDT min qty is usually 0.00001.
				// We should ideally normalize quantity.
				// For now using %.5f (0.00001) which is safe for BTC.

				priceStr := fmt.Sprintf("%.2f", executionPrice)
				clientOrderID := fmt.Sprintf("BUY_%d_L%d", time.Now().UnixMilli(), currentLevel)

				req := api.OrderRequest{
					Symbol:           s.Cfg.Symbol,
					Side:             "BUY",
					Type:             "LIMIT",
					TimeInForce:      "GTC",
					Quantity:         qtyStr,
					Price:            priceStr,
					NewClientOrderID: clientOrderID,
				}

				logger.Info("Attempting to Place Order", "qty", qtyStr, "price", priceStr)

				resp, err := s.Binance.CreateOrder(req)
				if err != nil {
					logger.Error("❌ Failed to create Buy Order", "error", err)
					return
				}

				logger.Info("✅ Buy Order Placed", "orderID", resp.OrderId, "status", resp.Status)

				// 2. Save to Transactions (Maker)
				// We save it as "Open" (or filled if it filled immediately).
				// Response gives Status.

				buyTx := model.Transaction{
					ID:                resp.ClientOrderId, // Use what we sent or what they returned
					TransactionID:     resp.ClientOrderId,
					Symbol:            s.Cfg.Symbol,
					Type:              "buy",
					Amount:            resp.OrigQty, // Use confirmed qty
					Price:             resp.Price,   // Use confirmed price
					StatusTransaction: "open",       // Assume open, updated via stream later?
					// If Status is FILLED, we mark as filled immediately?
					// Code processFills() handles updates. But if it's already filled, processFills might not catch it if we check CurrentPrice vs OrderPrice?
					// If filled immediately, we should mark filled.
					Notes:     fmt.Sprintf("Grid L%d (Maker)", currentLevel),
					CreatedAt: time.Now(),
					UpdatedAt: time.Now(),
				}

				if resp.Status == "FILLED" {
					buyTx.StatusTransaction = "filled"
					// We might need to handle "Maker Fill" logic (deducting balances etc)?
					// But balances are now synced from API!
					// So specific balance math in processFills is less critical for *Bot State*,
					// but critical for *Transaction Tracking* (profit calcs).
				}

				if err := s.TransactionRepo.Save(buyTx); err != nil {
					logger.Error("Failed to save transaction", "error", err)
				}

				logger.Info("📌 Maker Transaction Recorded", "level", currentLevel)

			} else {
				logger.Warn("Insufficient funds for new order", "needed", orderValue, "have", saldoUSDT)
				s.checkAndAlertLowUSDT(saldoUSDT, orderValue)
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

func (s *Strategy) calculateOrderValue(balance float64) float64 {
	rawOrderValue := balance * s.Cfg.PositionSizePct
	if rawOrderValue < s.Cfg.MinOrderValue {
		return s.Cfg.MinOrderValue
	}
	return rawOrderValue
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

// SyncOrdersOnStartup checks all 'open' orders in the repository against Binance API
// to catch any state changes that happened while the bot was offline.
func (s *Strategy) SyncOrdersOnStartup() {
	logger.Info("🔄 Syncing Open Orders with Binance...")

	transactions := s.TransactionRepo.GetAll()
	var syncedCount int

	for _, tx := range transactions {
		// We only care about Open Check for "buy" orders usually, as "sell" are takers (immediate).
		// But if we ever have "sell" limit orders, we should check them too.
		// For this strategy: Makers are "buy" limit orders. Takers are "sell" market orders (usually).
		// So checking "open" status is key.
		if tx.StatusTransaction == "open" {
			// Check status on Binance
			resp, err := s.Binance.GetOrder(tx.Symbol, tx.ID)
			if err != nil {
				logger.Error("⚠️ Failed to check order status on startup, skipping sync for this order", "orderID", tx.ID, "error", err)
				continue
			}

			// If status changed, update our repo
			if resp.Status != "NEW" && resp.Status != "PARTIALLY_FILLED" {
				logger.Info("🔄 Order Status Changed while offline", "orderID", tx.ID, "old_status", "open", "new_status", resp.Status)

				if resp.Status == "FILLED" {
					tx.StatusTransaction = "filled"
					tx.Price = resp.Price // Use confirmation price
					if resp.ExecutedQty != "" {
						tx.Amount = resp.ExecutedQty
					}
					tx.Notes += " | Synced On Startup"
					s.TransactionRepo.Update(tx)
					syncedCount++

					// Notify generic
					logger.Info("✅ Order synced as FILLED", "orderID", tx.ID)

				} else if resp.Status == "CANCELED" || resp.Status == "REJECTED" || resp.Status == "EXPIRED" {
					tx.StatusTransaction = "closed"
					tx.Notes += fmt.Sprintf(" | Synced On Startup: %s", resp.Status)
					s.TransactionRepo.Update(tx)
					syncedCount++

					logger.Info("🚫 Order synced as CLOSED/CANCELED", "orderID", tx.ID)
				}
			}
		}
	}

	logger.Info("✅ Startup Order Sync Completed", "updated_orders", syncedCount)
}

func (s *Strategy) checkAndAlertLowUSDT(currentBalance, required float64) {
	if time.Since(s.lastUSDTAlertTime) < 1*time.Hour {
		return
	}

	logger.Warn("⚠️ Alerting Low USDT Balance", "balance", currentBalance, "required", required)
	s.TelegramService.SendLowBalanceAlert("USDT", currentBalance, required)
	s.lastUSDTAlertTime = time.Now()
}

func (s *Strategy) checkLowBNB(bnbPrice float64) {
	if time.Since(s.lastBNBAlertTime) < 1*time.Hour {
		return
	}

	saldoUSDT := s.getBalance("USDT")
	calculated := saldoUSDT * s.Cfg.PositionSizePct
	if calculated < s.Cfg.MinOrderValue {
		calculated = s.Cfg.MinOrderValue
	}

	thresholdUSDT := calculated * 0.05 // 5% of order value

	bnbBalance := s.getBalance("BNB")
	bnbValueUSDT := bnbBalance * bnbPrice

	if bnbValueUSDT < thresholdUSDT {
		logger.Warn("⚠️ BNB Balance Low", "bnb_value_usdt", bnbValueUSDT, "threshold", thresholdUSDT)

		thresholdBNB := thresholdUSDT / bnbPrice
		s.TelegramService.SendLowBalanceAlert("BNB", bnbBalance, thresholdBNB)

		s.lastBNBAlertTime = time.Now()
	}
}
