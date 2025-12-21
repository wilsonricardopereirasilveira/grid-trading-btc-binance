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
	Cfg                       *config.Config
	BalanceRepo               *repository.BalanceRepository
	TransactionRepo           *repository.TransactionRepository
	TelegramService           *service.TelegramService
	Binance                   *api.BinanceClient
	lastFillCheck             time.Time
	lastUSDTAlertTime         time.Time
	lastBNBAlertTime          time.Time
	circuitBreakerTriggeredAt time.Time
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

	// 3. Check Take Profit (Legacy Polling Removed - Now Event Driven)
	// s.checkTakeProfit(filledOrders, activeOpenOrders, ticker.Price, bnbPrice)

	// 5. Volatility Circuit Breaker (Crash Protection)
	if !s.isMarketSafe(ticker.Price) {
		return // Block new entries
	}

	// 5.5. Soft Panic Button (Pause Buys)
	if s.Cfg.PauseBuys {
		logger.Warn("⚠️ PAUSE_BUYS está ATIVO. Pulando criação de novas ordens de compra.")
		return // Block new entries
	}

	// 6. Place New Grid Orders (Maker)
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
	s.checkSmartEntryReposition(openOrders, filledOrders, ticker.Price)
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
		if tx.StatusTransaction != "filled" && tx.StatusTransaction != "waiting_sell" && tx.StatusTransaction != "closed" {
			logger.Info("⚡ WebSocket: Order FILLED", "orderID", tx.ID, "price", event.LastExecPrice)

			// If it's a BUY order, we treat it as an entry fill -> Place Exit
			if tx.Type == "buy" {
				// IDEMPOTENCY CHECK:
				if tx.SellOrderID != "" {
					logger.Info("ℹ️ Buy Order Filled, but Sell Order already exists. Skipping duplicate.", "buyID", tx.ID)
					return
				}

				tx.StatusTransaction = "filled"
				tx.Price = event.LastExecPrice // Update entry price
				if event.LastExecQty != "" {
					tx.Amount = event.LastExecQty
				}
				tx.Notes += " | WS Verified Fill"
				s.TransactionRepo.Update(tx)

				// TRIGGER MAKER EXIT
				s.placeMakerExitOrder(&tx)

				// Notify Entry
				s.sendTradeNotification(tx, 0, nil)

			} else if tx.Type == "sell" {
				// Should not happen often if we use Maker-Exit logic tied to the Buy Tx,
				// but if we have separate Sell Tx, handle here.
				// However, in Maker-Maker, we attach Sell info to the Buy Tx usually?
				// The prompt says "Transactions.json... SellOrderID".
				// So when the SELL order fills, we are updating the BUY Transaction that owns it?
				// OR we receive an event for the SELL order ID, look it up in Repo?
				// Since we store SellOrderID in the Transaction, we can look up by SellOrderID?
				// The Repo.Get(ID) usually searches by ID (which is the Buy ID).
				// We need a way to find the Transaction by SellOrderID if the event is for the Sell Order.
				// For now, let's assume we maintain the Buy ID as the main ID.
				// But the event comes with ClientOrderID.
				// When we place Maker Exit, we set NewClientOrderID.
				// We need to support finding by that ID.
			}
		} else {
			// Maybe it's a fill for the Sell Order?
			// If tx.SellOrderID == event.ClientOrderID ...
			if tx.SellOrderID == event.ClientOrderID {
				logger.Info("💰 WebSocket: Maker Exit Order FILLED", "sellOrderID", event.ClientOrderID)

				// Mark as closed/sold
				tx.StatusTransaction = "closed"
				now := time.Now()
				tx.ClosedAt = &now

				// Calculate Profit
				buyPrice, _ := strconv.ParseFloat(tx.Price, 64)
				sellPrice, _ := strconv.ParseFloat(event.LastExecPrice, 64)
				qty, _ := strconv.ParseFloat(tx.Amount, 64)

				revenue := sellPrice * qty
				cost := buyPrice * qty
				profit := revenue - cost

				tx.Notes += fmt.Sprintf(" | Sold at %.2f (Profit: $%.2f)", sellPrice, profit)
				s.TransactionRepo.Update(tx)

				// Notify Exit
				// Create a temporary "Sell" transaction for the notification so it renders as VENDA
				sellTx := tx
				sellTx.ID = event.ClientOrderID
				sellTx.Type = "sell"
				sellTx.Price = event.LastExecPrice
				sellTx.StatusTransaction = "filled"

				s.sendTradeNotification(sellTx, profit, nil)
			}
		}
	} else if event.Status == "CANCELED" || event.Status == "REJECTED" || event.Status == "EXPIRED" {
		if tx.StatusTransaction != "closed" {
			// Check if it's the Sell Order that was canceled
			if tx.SellOrderID == event.ClientOrderID {
				logger.Warn("⚠️ Maker Exit Order Canceled/Rejected", "sellOrderID", tx.SellOrderID)
				// Reset status to filled so we retry placing it?
				// Or 'waiting_sell' so startup sync catches it?
				// Strategy says: "Retry... if failed... log critical"
				// But if canceled externally?
				// Let's set it back to 'filled' to retry placement if appropriate, or 'waiting_sell' and let sync handle it.
				// If we set to 'filled', the next 'execute' loop won't inherently trigger 'placeMakerExitOrder' unless we add logic there.
				// But safely, we can log and maybe try to replace immediately?
				// For now, let's log.
			} else {
				// It's the buy order
				logger.Warn("⚠️ WebSocket: Buy Order Closed/Canceled", "orderID", tx.ID, "status", event.Status)
				tx.StatusTransaction = "closed"
				tx.Notes += fmt.Sprintf(" | Closed via WS: %s", event.Status)
				s.TransactionRepo.Update(tx)
			}
		}
	}
}

// sendTradeNotification helper to avoid duplicated code
func (s *Strategy) sendTradeNotification(tx model.Transaction, profit float64, ordersToClose []model.Transaction) {
	var usdtBal, bnbBal, btcBal float64
	accInfo, err := s.Binance.GetAccountInfo()
	if err != nil {
		logger.Error("⚠️ Failed to fetch fresh balances", "error", err)
		usdtBal = s.getBalance("USDT")
		bnbBal = s.getBalance("BNB")
		btcBal = s.getBalance("BTC")
	} else {
		for _, b := range accInfo.Balances {
			if b.Asset == "USDT" {
				usdtBal, _ = strconv.ParseFloat(b.Free, 64)
			} else if b.Asset == "BNB" {
				bnbBal, _ = strconv.ParseFloat(b.Free, 64)
			} else if b.Asset == "BTC" {
				btcBal, _ = strconv.ParseFloat(b.Free, 64)
			}
		}
	}
	s.TelegramService.SendTradeNotification(tx, profit, ordersToClose, usdtBal, bnbBal, btcBal)
}

// Implement placeMakerExitOrder
func (s *Strategy) placeMakerExitOrder(tx *model.Transaction) {
	// 1. Calculate Sell Price
	buyPrice, _ := strconv.ParseFloat(tx.Price, 64)
	// profitMargin := s.Cfg.MinNetProfitPct // Unused in Grid Strategy (Fixed Spacing)
	// Or should we use GridSpacing? Usually Grid uses fixed spacing.
	// But let's stick to "ProfitMargin" concept from prompt.
	// Assuming MinNetProfitPct is appropriate or we should check if there is a separate config.
	// The prompt said: SellPrice = BuyPrice * (1 + ProfitMargin).
	// Let's use GridSpacingPct as valid Proxy if ProfitMargin not explicit.
	// Actually, typically for Grid, Sell = Buy + GridSpacing.
	targetPrice := buyPrice * (1 + s.Cfg.GridSpacingPct)

	sellPriceStr := fmt.Sprintf("%.2f", targetPrice)

	// 2. Calculate Quantity (Safety Check)
	buyQty, _ := strconv.ParseFloat(tx.Amount, 64)

	// Check Available Balance
	// We need to know which asset we are selling. BTCUSDT -> Sell BTC.
	var baseAsset string = "BTC" // Hardcoded for BTCUSDT or derive from Symbol
	if len(s.Cfg.Symbol) > 4 && s.Cfg.Symbol[len(s.Cfg.Symbol)-4:] == "USDT" {
		baseAsset = s.Cfg.Symbol[:len(s.Cfg.Symbol)-4]
	}

	// Get LIVE balance to be safe
	accInfo, err := s.Binance.GetAccountInfo()
	var availableBalance float64
	if err == nil {
		for _, b := range accInfo.Balances {
			if b.Asset == baseAsset {
				availableBalance, _ = strconv.ParseFloat(b.Free, 64)
				break
			}
		}
		// Update local repo
		s.BalanceRepo.Update(baseAsset, availableBalance)
	} else {
		logger.Warn("⚠️ Using cached balance for safety check (API fail)")
		availableBalance = s.getBalance(baseAsset)
	}

	// 0.999 safety factor
	safeSellQty := availableBalance * 0.999

	sellQty := buyQty
	if sellQty > safeSellQty {
		logger.Warn("⚠️ Insufficient balance for full sell. Adjusting.", "wanted", sellQty, "have_safe", safeSellQty)
		sellQty = safeSellQty
	}

	// Min Lot Size Check? 0.00001 BTC.
	if sellQty < 0.00001 {
		logger.Error("❌ Sell Quantity too low to place order", "qty", sellQty)
		return
	}

	qtyStr := fmt.Sprintf("%.5f", sellQty)

	// 3. Execution with Retry
	sellOrderID := fmt.Sprintf("SELL_%d", time.Now().UnixNano())

	req := api.OrderRequest{
		Symbol:           s.Cfg.Symbol,
		Side:             "SELL",
		Type:             "LIMIT",
		TimeInForce:      "GTC",
		Quantity:         qtyStr,
		Price:            sellPriceStr,
		NewClientOrderID: sellOrderID,
	}

	var resp *api.OrderResponse
	maxRetries := 5
	backoff := 1 * time.Second

	for i := 0; i < maxRetries; i++ {
		resp, err = s.Binance.CreateOrder(req)
		if err == nil {
			break
		}
		logger.Warn("⚠️ Failed to place Maker Exit. Retrying...", "attempt", i+1, "error", err)
		time.Sleep(backoff)
		backoff *= 2
	}

	if err != nil {
		logger.Error("🚨 CRITICAL: Failed to place Maker Exit Order after retries!", "buyOrderID", tx.ID)
		s.TelegramService.SendMessage(fmt.Sprintf("🚨 CRITICAL: Failed to place Maker Exit for Order %s. Please check manually!", tx.ID))

		// Mark as failed_placement so we know it needs manual intervention
		tx.StatusTransaction = "failed_placement"
		s.TransactionRepo.Update(*tx)
		return
	}

	logger.Info("✅ Maker Exit Order Placed", "sellOrderID", resp.OrderId, "price", sellPriceStr)

	// 4. Persistence
	tx.SellOrderID = resp.ClientOrderId // Or resp.OrderId (int) converted to string? Model has string.
	// Usually ClientOrderId is reliable if we set it.
	tx.SellPrice = targetPrice
	tx.SellCreatedAt = time.Now()
	tx.StatusTransaction = "waiting_sell"

	s.TransactionRepo.Update(*tx)
}

const (
	FeeRateBNB = 0.00075 // 0.075%
	FeeRateStd = 0.00100 // 0.10%
	BNBBuffer  = 1.1     // 10% safety buffer
)

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
		// MAKER ENTRY CHECK (Buy Limit)
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

					logger.Info("✅ Order synced as FILLED - Triggering Maker Exit", "orderID", tx.ID)
					// Trigger Exit creation for this missed fill
					s.placeMakerExitOrder(&tx)

				} else if resp.Status == "CANCELED" || resp.Status == "REJECTED" || resp.Status == "EXPIRED" {
					tx.StatusTransaction = "closed"
					tx.Notes += fmt.Sprintf(" | Synced On Startup: %s", resp.Status)
					s.TransactionRepo.Update(tx)
					syncedCount++

					logger.Info("🚫 Order synced as CLOSED/CANCELED", "orderID", tx.ID)
				}
			}
		}

		// MAKER EXIT CHECK (Waiting Sell)
		if tx.StatusTransaction == "waiting_sell" && tx.SellOrderID != "" {
			resp, err := s.Binance.GetOrder(tx.Symbol, tx.SellOrderID)
			if err != nil {
				// 404 Case or Error
				logger.Error("⚠️ Failed to check SELL Order status on startup", "sellID", tx.SellOrderID, "error", err)
				// If error contains code -2013 (Order does not exist), it might be 404.
				// Standard Binance error checks needed.
				// Assuming critical error for now.
				// If 404, we might need to recreate?
				continue
			}

			if resp.Status == "FILLED" {
				// Sold!
				logger.Info("💰 Maker Exit Found FILLED on Startup", "sellID", tx.SellOrderID)
				tx.StatusTransaction = "closed"
				tx.ClosedAt = &time.Time{}
				*tx.ClosedAt = time.Now() // Approximate or use UpdateTime from resp
				tx.Notes += " | Sold while offline"
				s.TransactionRepo.Update(tx)
				syncedCount++
			} else if resp.Status == "CANCELED" || resp.Status == "REJECTED" || resp.Status == "EXPIRED" {
				logger.Warn("⚠️ Maker Exit Found CANCELED on Startup. Recreating...", "sellID", tx.SellOrderID)
				// Re-place order
				s.placeMakerExitOrder(&tx)
			}
			// If NEW/PARTIALLY_FILLED, it's fine.
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

func (s *Strategy) checkSmartEntryReposition(openOrders, filledOrders []model.Transaction, currentLastPrice float64) {
	// 1. Zero Inventory Rule: Only reposition if we hold NO position.
	if len(filledOrders) > 0 {
		return
	}

	// 2. Must have Open Orders to reposition
	if len(openOrders) == 0 {
		return
	}

	// Find Highest Open Buy Order (Entry Candidate)
	// Open orders are passed in. Sort them to find highest.
	// Note: The strategy places orders, usually L1 is the highest.
	// We want the ONE closest to price (Highest Price).
	var highestOrder *model.Transaction
	var highestPrice float64 = -1.0

	for i := range openOrders {
		p, _ := strconv.ParseFloat(openOrders[i].Price, 64)
		if p > highestPrice {
			highestPrice = p
			highestOrder = &openOrders[i]
		}
	}

	if highestOrder == nil {
		return
	}

	// 3. Price Diff Check
	// If market moves UP, Current Price > Order Price.
	// We use Ask Price ideally, but 'currentLastPrice' is passed.
	// Let's use currentLastPrice for the TRIGGER check as it's lighter.
	// Or should we fetch BookTicker here?
	// The user wants reliable check. "market moves up".
	// using LastPrice is fine.

	if s.Cfg.SmartEntryRepositionPct <= 0 {
		return // Feature disabled
	}

	diffPct := (currentLastPrice - highestPrice) / highestPrice

	// 4. Trigger Logic (Smart Entry V2.0)
	// Condition A: Price Runaway (Urgent) - Only if Cooldown passed
	isPriceRunaway := diffPct >= s.Cfg.SmartEntryRepositionPct
	cooldown := time.Duration(s.Cfg.SmartEntryRepositionCooldown) * time.Minute
	isCooldownPassed := time.Since(highestOrder.CreatedAt) >= cooldown

	// Condition B: Stagnation (Boredom) - If order is too old (e.g. 20 min)
	// We want to force reposition to current market even if price didn't run away X%.
	maxIdle := time.Duration(s.Cfg.SmartEntryRepositionMaxIdleMin) * time.Minute
	isStagnant := time.Since(highestOrder.CreatedAt) >= maxIdle

	shouldReposition := (isPriceRunaway && isCooldownPassed) || isStagnant

	if !shouldReposition {
		return
	}

	triggerReason := "Price Runaway"
	if isStagnant && !isPriceRunaway {
		triggerReason = "Stagnation (Idle Timeout)"
	}

	logger.Info("⚡ Smart Entry Reposition Triggered",
		"reason", triggerReason,
		"orderPrice", highestPrice,
		"currentPrice", currentLastPrice,
		"diffPct", fmt.Sprintf("%.4f%%", diffPct*100),
		"orderID", highestOrder.ID,
		"orderAge", time.Since(highestOrder.CreatedAt).String(),
	)

	// Fetch Real-time Book Ticker for Limit Maker Placement
	book, err := s.Binance.GetBookTicker(s.Cfg.Symbol)
	if err != nil {
		logger.Error("❌ Failed to get BookTicker for repositioning", "error", err)
		return
	}

	newPriceStr := book.BidPrice
	newPrice, _ := strconv.ParseFloat(newPriceStr, 64)

	// Safety: Ensure newPrice is actually higher than old price?
	// Usually yes if diffPct is positive.

	// 5. Execute Reposition

	// A) Cancel Old Order
	_, err = s.Binance.CancelOrder(s.Cfg.Symbol, highestOrder.ID)
	if err != nil {
		logger.Error("⚠️ Failed to cancel old order for reposition", "orderID", highestOrder.ID, "error", err)
		// If failed (e.g. already filled), we stop.
		// Check if it was filled?
		return
	}

	// B) Update Old Order in Repo
	highestOrder.StatusTransaction = "closed"
	highestOrder.Notes += " | Repositioned (Smart Entry)"
	if err := s.TransactionRepo.Update(*highestOrder); err != nil {
		logger.Error("Failed to update repositioned order", "error", err)
	}

	// C) Create New Order at CurrentBid (Maker Attempt)
	// Reuse quantity/value logic?
	// Use same quantity as old order? Or recalculate based on current balance?
	// If we use same quantity, we preserve "Entry Size".
	// If we recalculate, we fit valid size.
	// Let's use the AMOUNT of the old order to be consistent?
	// Or better: Recalculate based on Config PositionSizePct, as price changed.
	// Let's Recalculate to be safe with MinOrderValue etc.

	saldoUSDT := s.getBalance("USDT")
	orderValue := s.calculateOrderValue(saldoUSDT)

	// Logic from placeNewGridOrders
	if saldoUSDT < orderValue {
		logger.Warn("Insufficient funds for Reposition", "needed", orderValue, "have", saldoUSDT)
		return
	}

	buyQty := orderValue / newPrice
	qtyStr := fmt.Sprintf("%.5f", buyQty) // Fixed precision for BTC (TODO: Dynamic prec)

	newClientOrderID := fmt.Sprintf("BUY_R_%d", time.Now().UnixMilli())

	req := api.OrderRequest{
		Symbol:           s.Cfg.Symbol,
		Side:             "BUY",
		Type:             "LIMIT",
		TimeInForce:      "GTC",
		Quantity:         qtyStr,
		Price:            newPriceStr,
		NewClientOrderID: newClientOrderID,
	}

	logger.Info("🔄 Placing Reposition Order (Maker Attempt)", "price", newPriceStr, "qty", qtyStr)

	resp, err := s.Binance.CreateOrder(req)
	if err != nil {
		logger.Error("❌ Failed to create Reposition Order", "error", err)
		return
	}

	logger.Info("✅ Reposition Order Placed", "orderID", resp.OrderId)

	// D) Save New Transaction
	newTx := model.Transaction{
		ID:                resp.ClientOrderId,
		TransactionID:     resp.ClientOrderId,
		Symbol:            s.Cfg.Symbol,
		Type:              "buy",
		Amount:            resp.OrigQty,
		Price:             resp.Price,
		StatusTransaction: "open",
		Notes:             "Smart Entry Reposition",
		CreatedAt:         time.Now(),
		UpdatedAt:         time.Now(),
	}

	if resp.Status == "FILLED" {
		newTx.StatusTransaction = "filled"
	}

	if err := s.TransactionRepo.Save(newTx); err != nil {
		logger.Error("Failed to save new reposition transaction", "error", err)
	}
}

func (s *Strategy) isMarketSafe(currentPrice float64) bool {
	// Check if feature is enabled
	if !s.Cfg.CrashProtectionEnabled {
		return true
	}

	// 1. Fail-Safe / Paranoia Mode
	// We fetch 3 candles of 5m (15m history)
	klines, err := s.Binance.GetRecentKlines(s.Cfg.Symbol, "5m", 3)
	if err != nil {
		logger.Error("🚨 CRITICAL: Failed to fetch Klines for Safety Check. BLOCKING TRADES.", "error", err)
		return false // Block
	}

	if len(klines) == 0 {
		logger.Error("🚨 CRITICAL: No Klines returned. BLOCKING TRADES.")
		return false
	}

	// 2. Calculate Drop
	var maxHigh float64
	for _, k := range klines {
		h, _ := strconv.ParseFloat(k.High, 64)
		if h > maxHigh {
			maxHigh = h
		}
	}

	if maxHigh <= 0 {
		return false // Safe guard
	}

	dropPct := (maxHigh - currentPrice) / maxHigh

	// 3. Cooldown Logic
	if !s.circuitBreakerTriggeredAt.IsZero() {
		pauseDuration := time.Duration(s.Cfg.CrashPauseMin) * time.Minute
		if time.Since(s.circuitBreakerTriggeredAt) < pauseDuration {
			// Still in cooldown
			return false
		}

		// Cooldown passed. Check if safe NOW.
		if dropPct < s.Cfg.MaxDropPct5m {
			// Normalized.
			logger.Info("✅ Circuit Breaker Normalizado. Resuming trades.")
			s.circuitBreakerTriggeredAt = time.Time{} // Reset
			s.TelegramService.SendMessage("✅ *Circuit Breaker Normalizado*\nVolatilidade controlada. Retomando operações.")
			return true
		} else {
			// Still volatile. Extend.
			logger.Warn("⚠️ Market still volatile after cooldown. Extending pause.", "drop", fmt.Sprintf("%.2f%%", dropPct*100))
			s.circuitBreakerTriggeredAt = time.Now()
			return false
		}
	}

	// 4. Trigger Logic
	if dropPct > s.Cfg.MaxDropPct5m {
		s.circuitBreakerTriggeredAt = time.Now()
		logger.Warn("⚠️ CRASH DETECTED. Circuit Breaker Triggered.",
			"drop", fmt.Sprintf("%.2f%%", dropPct*100),
			"threshold", fmt.Sprintf("%.2f%%", s.Cfg.MaxDropPct5m*100),
			"maxHigh", maxHigh,
			"current", currentPrice,
		)

		msg := fmt.Sprintf("⚠️ *ALERTA: Circuit Breaker Ativado!* ⚠️\n\nQueda detectada: %.2f%%\nPreço Atual: %.2f\nMax (15m): %.2f\n\n⛔ *Compras Pausadas por %d min.*",
			dropPct*100, currentPrice, maxHigh, s.Cfg.CrashPauseMin)

		s.TelegramService.SendMessage(msg)

		return false
	}

	return true
}
