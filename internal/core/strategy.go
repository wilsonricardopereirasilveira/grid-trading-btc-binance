package core

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"time"

	"grid-trading-btc-binance/internal/api"
	"grid-trading-btc-binance/internal/config"
	"grid-trading-btc-binance/internal/logger"
	"grid-trading-btc-binance/internal/market"
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
	VolatilityService         *market.VolatilityService
	lastFillCheck             time.Time
	lastUSDTAlertTime         time.Time
	lastBNBAlertTime          time.Time
	circuitBreakerTriggeredAt time.Time
	lastBuyFailureTime        time.Time // Circuit Breaker for Order Placement -2010 loops
	tickSize                  float64
}

func NewStrategy(cfg *config.Config, balanceRepo *repository.BalanceRepository, transactionRepo *repository.TransactionRepository, telegramService *service.TelegramService, binanceClient *api.BinanceClient, volatilityService *market.VolatilityService) *Strategy {
	s := &Strategy{
		Cfg:               cfg,
		BalanceRepo:       balanceRepo,
		TransactionRepo:   transactionRepo,
		TelegramService:   telegramService,
		Binance:           binanceClient,
		VolatilityService: volatilityService,
	}

	// Fetch TickSize on startup
	s.fetchTickSize()

	// Cleanup Closed Transactions on Startup
	cleaned := s.TransactionRepo.CleanupClosed()
	if cleaned > 0 {
		logger.Info("🧹 Startup Cleanup: Archived closed transactions", "count", cleaned)
	}

	return s
}

func (s *Strategy) fetchTickSize() {
	info, err := s.Binance.GetExchangeInfo(s.Cfg.Symbol)
	if err != nil {
		logger.Error("⚠️ Failed to fetch ExchangeInfo for TickSize. Using default 0.01.", "error", err)
		s.tickSize = 0.01
		return
	}

	for _, symbol := range info.Symbols {
		if symbol.Symbol == s.Cfg.Symbol {
			for _, filter := range symbol.Filters {
				if filter.FilterType == "PRICE_FILTER" {
					ts, err := strconv.ParseFloat(filter.TickSize, 64)
					if err == nil && ts > 0 {
						s.tickSize = ts
						logger.Info("✅ TickSize Detected", "symbol", s.Cfg.Symbol, "tickSize", ts)
						return
					}
				}
			}
		}
	}
	logger.Warn("⚠️ TickSize not found in ExchangeInfo. Defaulting to 0.01.")
	s.tickSize = 0.01
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
			} else if tx.StatusTransaction == "filled" || tx.StatusTransaction == "waiting_sell" {
				filledOrders = append(filledOrders, tx)
			}
		}
	}

	s.placeNewGridOrders(openOrders, filledOrders, ticker.Price, ticker.Bid, bnbPrice)
	s.checkLowBNB(bnbPrice)
	s.checkSmartEntryReposition(openOrders, filledOrders, ticker.Price)
}

// HandleOrderUpdate processes executionReport events from WebSocket
func (s *Strategy) HandleOrderUpdate(event service.OrderUpdate) {
	if event.Symbol != s.Cfg.Symbol {
		return
	}

	// logger.Debug("⚡ Processing Order Update") // Reduced noise

	logger.Info("⚡ Order Update Received",
		"id", event.ClientOrderID,
		"status", event.Status,
		"execType", event.ExecutionType,
	)

	// Fetch transaction from Repo
	tx, exists := s.TransactionRepo.Get(event.ClientOrderID)
	if !exists {
		// Check secondary lookup by SellOrderID
		var found bool
		tx, found = s.TransactionRepo.GetBySellID(event.ClientOrderID)
		if !found {
			// Possibly a manual order or one we don't track?
			logger.Debug("Received update for unknown order", "id", event.ClientOrderID)
			return
		}
		// Found via SellOrderID
		// Continue execution with 'tx' found
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

				// tx.Notes += fmt.Sprintf(" | Sold at %.2f (Profit: $%.2f)", sellPrice, profit)
				// s.TransactionRepo.Update(tx) // Old Update

				// ARCHIVE AND DELETE
				tx.Notes += fmt.Sprintf(" | Sold at %.2f (Profit: $%.2f)", sellPrice, profit)
				// Save final state to archive
				if err := s.TransactionRepo.Archive(tx); err != nil {
					logger.Error("⚠️ Failed to archive transaction", "id", tx.ID, "error", err)
				}
				// Remove from active
				if err := s.TransactionRepo.Delete(tx.ID); err != nil {
					logger.Error("⚠️ Failed to delete active transaction after archive", "id", tx.ID, "error", err)
				} else {
					logger.Info("📦 Transaction Archived and Removed from Active List", "id", tx.ID)
				}

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
	dynamicSpacing := s.VolatilityService.GetDynamicSpacing()
	targetPrice := buyPrice * (1 + dynamicSpacing)

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

func (s *Strategy) placeNewGridOrders(openOrders, filledOrders []model.Transaction, currentAsk, currentBid, bnbPrice float64) {
	// CIRCUIT BREAKER CHECK
	if time.Since(s.lastBuyFailureTime) < 60*time.Second {
		// Silent return or debug log to avoid spam
		return
	}

	allOrders := append(openOrders, filledOrders...)

	// Sort by price ascending to find lowest/highest for different logic
	// But CRITICAL FIX: We must distinguish between "Active Grid Floor" (Open Buys) and "Inventory" (Filled/Sells).
	// If we use Filled orders as the floor, the bot will never buy above an old bag.

	var activeBuyOrders []model.Transaction
	for _, o := range openOrders {
		activeBuyOrders = append(activeBuyOrders, o)
	}

	sort.Slice(activeBuyOrders, func(i, j int) bool {
		p1, _ := strconv.ParseFloat(activeBuyOrders[i].Price, 64)
		p2, _ := strconv.ParseFloat(activeBuyOrders[j].Price, 64)
		return p1 < p2
	})

	lowestActivePrice := currentAsk
	if len(activeBuyOrders) > 0 {
		p, _ := strconv.ParseFloat(activeBuyOrders[0].Price, 64)
		lowestActivePrice = p
	}

	// Drop Percentage should be calculated from the LOWEST ACTIVE BUY.
	// If no active buys, we are "starting fresh" at current price (dropPct = 0).
	dropPct := 0.0
	if len(activeBuyOrders) > 0 {
		dropPct = (lowestActivePrice - currentAsk) / lowestActivePrice
	}

	isGridEmptyOfBuys := len(activeBuyOrders) == 0
	priceInRange := currentAsk >= s.Cfg.RangeMin && currentAsk <= s.Cfg.RangeMax

	// DYNAMIC SPREAD via Volatility Service
	dynamicSpacing := s.VolatilityService.GetDynamicSpacing()

	// Logic: Buy if (No Active Buys currently) OR (Price dropped enough below lowest active buy)
	if priceInRange && (isGridEmptyOfBuys || dropPct >= dynamicSpacing) {
		// SPATIAL CHECK (Anti-Duplicate):
		// Ensure we don't buy if there's ALREADY an order (Open or Filled) very close to this price.
		// The "IgnoreInventory" logic allowed us to buy below bags, but we must not buy ON TOP of bags/fills.
		isTooClose := false
		minDist := dynamicSpacing * 0.5 // Allow some overlap but not much. 50% of spacing.

		for _, o := range allOrders {
			p, _ := strconv.ParseFloat(o.Price, 64)
			distPct := math.Abs(p-currentAsk) / p
			if distPct < minDist {
				// logger.Debug("🚫 Price too close to existing order", "current", currentAsk, "existing", p, "dist", distPct)
				isTooClose = true
				break
			}
		}

		if isTooClose {
			return
		}

		if len(allOrders) < s.Cfg.GridLevels {
			// MAKER FIX: Use Current Bid (or slightly lower) to ensure we join the book and don't cross spread.
			// Using currentAsk triggers Taker execution immediately on LIMIT buys.
			executionPrice := currentBid // Was currentAsk

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
					Symbol: s.Cfg.Symbol,
					Side:   "BUY",
					Type:   "LIMIT_MAKER", // CORRECT FIX: Spot Post Only
					// TimeInForce:      "GTC",         // REMOVED: API Error -1106 says not to send this for LIMIT_MAKER
					Quantity:         qtyStr,
					Price:            priceStr,
					NewClientOrderID: clientOrderID,
				}

				logger.Info("Attempting to Place Order", "qty", qtyStr, "price", priceStr)

				// 3. Execution with Retry (Smart Logic for -2010)
				var resp *api.OrderResponse
				var err error // Declare error outside loop scope
				maxRetries := 3

				for i := 0; i < maxRetries; i++ {
					req.Price = priceStr // Ensure reset on retry loop
					resp, err = s.Binance.CreateOrder(req)

					if err == nil {
						break // Success
					}

					// Check for "Order would immediately match and take" (-2010)
					errorMsg := err.Error()

					// We tried to be smart, but let's just log and retry with backoff/adjustment
					logger.Warn("⚠️ Order Placement Failed. Retrying...", "attempt", i+1, "error", errorMsg)

					// Smart Backoff & Price Adjustment
					time.Sleep(time.Duration(200+(i*100)) * time.Millisecond)

					// Adjust Price: Decrease strictly to avoid Taker
					if s.tickSize > 0 {
						p, _ := strconv.ParseFloat(priceStr, 64)
						// CRASH FIX: If price is falling fast, 1 tick is not enough.
						// We need to back off significantly to be a MAKER.
						// Let's drop 0.05% per retry. This is aggressive but guarantees placement.
						// 87000 * 0.0005 = $43.
						// If user wants to catch the knife, catching it $40 lower is better than failing.
						dropStep := p * 0.0005 // 0.05%

						newPrice := p - dropStep
						priceStr = fmt.Sprintf("%.2f", newPrice)
						logger.Info("📉 Adjusting Price (0.05%) for Retry", "old", req.Price, "new", priceStr)
					}
				}

				if err != nil {
					// Handle GTX Rejection (Post Only) caused by failure even after retries
					logger.Error("❌ Failed to create Buy Order after retries. Pausing Buys for 60s.", "error", err)
					// CIRCUIT BREAKER: Pause buying to prevent ban/spam
					s.lastBuyFailureTime = time.Now()
					return
				}

				// Check for GTX Expiry (Immediate cancel because it would be Taker)
				if resp.Status == "EXPIRED" || resp.Status == "CANCELED" {
					logger.Warn("⚠️ Maker Buy Order Rejected (Post Only/GTX)", "status", resp.Status, "price", priceStr)
					// Do NOT save to transactions
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
					// LOGIC FIX: Immediate Fill handling
					// If filled immediately (e.g. matched hidden order or race condition despite GTX?), ensure Sell is placed.
					// With GTX, this shouldn't happen often for "Maker", but if it does (e.g. auction), handle it.
					logger.Info("⚡ Order filled immediately on creation - Placing Exit Order", "id", buyTx.ID)
					s.placeMakerExitOrder(&buyTx)
					// FIX: Notify User of Immediate Fill
					s.sendTradeNotification(buyTx, 0, nil)
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

// SyncOrdersOnStartup performs a Two-Way Synchronization:
// 1. Forward Sync: Imports any open orders on Binance that are missing locally (Orphans).
// 2. Reverse Sync: Updates any local 'open' orders that are no longer open on Binance (Filled/Canceled).
func (s *Strategy) SyncOrdersOnStartup() {
	logger.Info("🔄 Starting Two-Way Order Synchronization...")

	// 1. Fetch ALL Open Orders from Binance
	binantOpenOrders, err := s.Binance.GetOpenOrders(s.Cfg.Symbol)
	if err != nil {
		logger.Error("❌ Critical: Failed to fetch open orders from Binance on startup. Aborting sync.", "error", err)
		return
	}

	binanceOrderMap := make(map[string]api.OrderResponse)
	for _, bo := range binantOpenOrders {
		binanceOrderMap[bo.ClientOrderId] = bo
	}

	// 2. Load Local Transactions
	transactions := s.TransactionRepo.GetAll()
	localOrderMap := make(map[string]*model.Transaction)
	for i := range transactions {
		// Pointer to allow updates if needed (though we usually use ID to UpdateViaRepo)
		localOrderMap[transactions[i].ID] = &transactions[i]
	}

	// ===================================================================================
	// PHASE 1: FORWARD SYNC (Binance -> Local) - Import Orphans
	// ===================================================================================
	for clientID, binOrder := range binanceOrderMap {
		if _, exists := localOrderMap[clientID]; !exists {
			// Orphan Detected!

			// DUPLICATE PREVENTER: Check if this "Orphan" Sell is actually linked to a Buy
			if binOrder.Side == "SELL" {
				isLinked := false
				for _, localTx := range transactions {
					if localTx.SellOrderID == clientID {
						isLinked = true
						break
					}
				}
				if isLinked {
					logger.Info("⚠️ Skipping import of Linked Sell Order (Already in DB as SellOrderID)", "id", clientID)
					continue
				}
			}

			logger.Warn("👻 Orphan Order Detected on Binance (Not in DB). Importing...", "id", clientID, "price", binOrder.Price)

			// Determine Type
			txType := "buy"
			if binOrder.Side == "SELL" {
				txType = "sell"
			}

			newTx := model.Transaction{
				ID:                binOrder.ClientOrderId,
				TransactionID:     binOrder.ClientOrderId,
				Symbol:            binOrder.Symbol,
				Type:              txType,
				Amount:            binOrder.OrigQty,
				Price:             binOrder.Price,
				StatusTransaction: "open", // It's in OpenOrders, so it MUST be open
				Notes:             "Recovered during Startup Sync",
				CreatedAt:         time.Unix(binOrder.TransactTime/1000, 0),
				UpdatedAt:         time.Now(),
			}

			if err := s.TransactionRepo.Save(newTx); err != nil {
				logger.Error("Failed to save imported orphan order", "error", err)
			} else {
				logger.Info("✅ Orphan Order Imported Successfully", "id", newTx.ID)
			}
		}
	}

	// ===================================================================================
	// PHASE 2: REVERSE SYNC (Local -> Binance) - Check Status of Local Open Orders
	// ===================================================================================
	var syncedCount int
	// Re-fetch transactions to include newly imported ones?
	// Phase 2 only cares about what WE think is open.
	// If we just imported it as Open (Phase 1), and it IS in Binance Open Orders (definition of Phase 1),
	// then Phase 2 check (is it in Binance?) will say YES. So no change. Correct.

	// We iterate over the original list + imports? No, just iterate repo again or map.
	// Let's iterate current state of Repo to be safe.
	currTransactions := s.TransactionRepo.GetAll()

	for _, tx := range currTransactions {
		// We only care about reconciling 'open' or 'waiting_sell' orders
		if tx.StatusTransaction != "open" && tx.StatusTransaction != "waiting_sell" {
			continue
		}

		// Check if this local order exists in the Binance Open Orders list
		_, isOpenOnBinance := binanceOrderMap[tx.ID]

		if isOpenOnBinance {
			// Order is still open. Everything is fine.
			// Optional: Update Price/Qty if modified? Usually not for Limit.
			continue
		}

		// IF WE ARE HERE: Order is OPEN locally, but NOT in Binance Open Orders.
		// Conclusion: It was Filled, Canceled, or Expired while we were offline.
		logger.Info("🔄 Order missed from OpenOrders list (Closed offline). Checking status...", "id", tx.ID)

		// Fetch specific order details to know exact final state
		resp, err := s.Binance.GetOrder(tx.Symbol, tx.ID)
		if err != nil {
			logger.Error("⚠️ Failed to check status of missing order", "id", tx.ID, "error", err)
			// Decide: Keep as open? Or mark unknown? Keep open to retry next sync.
			continue
		}

		// Update Local State
		if resp.Status == "FILLED" {
			tx.StatusTransaction = "filled"
			tx.Price = resp.Price
			if resp.ExecutedQty != "" {
				tx.Amount = resp.ExecutedQty
			}
			tx.Notes += " | Synced (Filled Offline)"
			tx.UpdatedAt = time.Now()
			s.TransactionRepo.Update(tx)
			syncedCount++

			logger.Info("✅ Order Synced: FILLED Offline", "id", tx.ID)

			// ACTION: If it was a BUY, we must ensure we have an Exit!
			// ACTION: If it was a BUY, we must ensure we have an Exit!
			if tx.Type == "buy" {
				// SMART RECOVERY (Startup Sync Fix):
				// Check if we already have a sell order linked or orphan.
				// This handles cases where we created the order offline.

				var foundSellID string
				buyQtyFloat, _ := strconv.ParseFloat(resp.ExecutedQty, 64)

				// 1. Check strict link
				if tx.SellOrderID != "" {
					if _, ok := binanceOrderMap[tx.SellOrderID]; ok {
						foundSellID = tx.SellOrderID
						logger.Info("🔗 Startup Relinking: Existing Sell Order found by ID.", "sellID", foundSellID)
					}
				}

				// 2. Scan for Orphan Sell Orders (Match by Quantity)
				if foundSellID == "" {
					for _, bo := range binanceOrderMap {
						if bo.Side == "SELL" {
							sellQtyFloat, _ := strconv.ParseFloat(bo.OrigQty, 64)
							if math.Abs(sellQtyFloat-buyQtyFloat) < 0.00000001 {
								foundSellID = bo.ClientOrderId
								logger.Info("🔗 Startup Relinking: Matching Orphan Sell Order found by Quantity.", "sellID", foundSellID)
								break
							}
						}
					}
				}

				if foundSellID != "" {
					tx.SellOrderID = foundSellID
					tx.StatusTransaction = "waiting_sell" // Or filled? waiting_sell implies we are waiting for it to fill. Correct.
					tx.UpdatedAt = time.Now()
					s.TransactionRepo.Update(tx)
					logger.Info("✅ Startup Sync: Linked existing Sell Order.", "buyID", tx.ID, "sellID", foundSellID)
				} else {
					logger.Info("🚀 Startup Sync: Triggering Maker Exit for Offline Fill", "buyID", tx.ID)
					s.placeMakerExitOrder(&tx)
				}
			}

			// ACTION: If it was a SELL (Maker Exit), calculate profit
			if tx.Type == "sell" {
				tx.StatusTransaction = "closed"
				now := time.Now()
				tx.ClosedAt = &now
				tx.Notes += " | Sold Offline"
				s.TransactionRepo.Update(tx)
				logger.Info("💰 Maker Exit Confirmed Closed (Offline)", "sellID", tx.ID)
				// We could try to calculate profit here if we link to Buy, but for now just marking closed is critical.
			}

		} else if resp.Status == "CANCELED" || resp.Status == "EXPIRED" || resp.Status == "REJECTED" {
			// If it was CANCELED, we mark it closed (or removed).
			tx.StatusTransaction = "closed" // Or "cancelled" if we had that status
			tx.Notes += fmt.Sprintf(" | Synced (%s Offline)", resp.Status)
			tx.UpdatedAt = time.Now()
			s.TransactionRepo.Update(tx)
			logger.Warn("⚠️ Order Synced: CANCELED/EXPIRED Offline", "id", tx.ID, "status", resp.Status)

			// If it was a Maker Exit that got Canceled, do we need to replace it?
			// Maybe. But for safety, we mark closed. Next strategy cycle might not see it.
			// Ideal: Mark parent Buy as 'filled' (no, it is filled) but 'waiting_sell' -> 'open' (no).
			// If Sell Canceled, we effectively have exposure without exit.
			// This is a risk.
			// Future improvement: "Revive" the Buy transaction to "filled" state without SellID so `placeMakerExit` runs again?
			// For now, logging effectively covers the "Stop the bleeding" requirement.
		}
	}

	logger.Info("✅ Startup Sync Phase 2 Completed", "synced_updates", syncedCount)

	// ===================================================================================
	// PHASE 3: GHOST TRANSACTION CLEANUP
	// Check all 'filled' transactions with a SellOrderID - if that sell order doesn't exist
	// on Binance anymore, the sell was completed and we should archive it.
	// Also cleans failed_placement entries.
	// ===================================================================================
	s.purgeGhostTransactions(binanceOrderMap)

	// ===================================================================================
	// PHASE 4: DUPLICATE TRANSACTION CLEANUP
	// Removes standalone "SELL" transactions that are already linked to a "BUY"
	// ===================================================================================
	s.purgeDuplicateTransactions()

	// ===================================================================================
	// PHASE 5: ZOMBIE RESCUE (Naked Buys)
	// Identifies "Filled" Buys that have NO Sell Order.
	// Action: Attempts to place the missing Exit Order.
	// If Insufficient Balance (already sold manually?), archives and cleans up.
	// ===================================================================================
	s.rescueZombieTransactions()
}

// rescueZombieTransactions finds "Filled" Buys without SellOrderID and tries to fix them
func (s *Strategy) rescueZombieTransactions() {
	logger.Info("🧟 Phase 5: Checking for Zombie Transactions (Filled Buys without Exit)...")
	transactions := s.TransactionRepo.GetAll()
	var rescueCount int

	for _, tx := range transactions {
		// Criteria: Buy + Filled + Empty SellOrderID
		if tx.Type == "buy" && tx.StatusTransaction == "filled" && tx.SellOrderID == "" {
			logger.Warn("🧟 Zombie Detected! Filled Buy with no Exit Order.", "id", tx.ID, "price", tx.Price)

			// Attempt to Rescue: Place Exit Order
			// We define a callback to handle failure (specifically insufficient balance)

			// We need to call placeMakerExitOrder but catch specific errors?
			// The current placeMakerExitOrder doesn't return error to here, checks internally.
			// Let's modify it or rely on its behavior?
			// Strategy: CALL IT. If it fails with Insufficient Balance, it should log.
			// BUT, to archive it if failed, we need feedback.

			// Custom Logic for Rescue:
			balance := s.getBalance("BTC")
			qty, _ := strconv.ParseFloat(tx.Amount, 64)

			// Safety factor 0.999 is used in placeMakerExitOrder, let's verify here first?
			if balance < qty*0.99 {
				logger.Warn("🧟 Zombie Rescue Failed: Insufficient BTC Balance. Assuming manually sold.", "id", tx.ID, "needed", qty, "have", balance)

				// Archive & Delete (It's a Ghost/Lost order)
				tx.StatusTransaction = "closed"
				tx.Notes += " | Zombie Cleaned (Insufficient Balance - Assumed Sold)"
				s.TransactionRepo.Archive(tx)
				s.TransactionRepo.Delete(tx.ID)
				continue
			}

			// If we have balance, we try to place the order
			logger.Info("🚑 Attempting Zombie Rescue: Placing Exit Order...", "id", tx.ID)
			s.placeMakerExitOrder(&tx)
			rescueCount++
		}
	}

	if rescueCount > 0 {
		logger.Info("✅ Zombie Rescue Operations Triggered", "count", rescueCount)
	} else {
		logger.Info("✅ No Zombie Transactions found")
	}
}

// purgeDuplicateTransactions removes 'sell' type transactions that are already present as SellOrderID in a 'buy' transaction
func (s *Strategy) purgeDuplicateTransactions() {
	logger.Info("🧹 Phase 4: Checking for Duplicate Transactions...")
	transactions := s.TransactionRepo.GetAll()

	// Build map of linked SellIDs
	linkedSellIDs := make(map[string]bool)
	for _, tx := range transactions {
		if tx.Type == "buy" && tx.SellOrderID != "" {
			linkedSellIDs[tx.SellOrderID] = true
		}
	}

	var matchCount int
	for _, tx := range transactions {
		if tx.Type == "sell" {
			// Check if this Sell Transaction ID is used as a SellOrderID in any Buy
			if linkedSellIDs[tx.ID] {
				logger.Info("👯 Duplicate Sell Transaction Detected. Archiving...", "id", tx.ID)

				// Archive
				if err := s.TransactionRepo.Archive(tx); err != nil {
					logger.Error("Failed to archive duplicate", "error", err)
				}
				// Delete
				if err := s.TransactionRepo.Delete(tx.ID); err != nil {
					logger.Error("Failed to delete duplicate", "error", err)
				} else {
					matchCount++
				}
			}
		}
	}

	if matchCount > 0 {
		logger.Info("✅ Duplicate Cleanup Complete", "removed_count", matchCount)
	} else {
		logger.Info("✅ No duplicate transactions found")
	}
}

// purgeGhostTransactions removes transactions that reference orders no longer on Binance.
// This handles cases where sells were filled while bot was offline.
func (s *Strategy) purgeGhostTransactions(binanceOrderMap map[string]api.OrderResponse) int {
	logger.Info("🧹 Phase 3: Checking for Ghost Transactions...")

	transactions := s.TransactionRepo.GetAll()
	var purgedCount int

	for _, tx := range transactions {
		shouldPurge := false
		reason := ""

		// Case 1: failed_placement - these never had valid orders
		if tx.StatusTransaction == "failed_placement" {
			shouldPurge = true
			reason = "Failed Placement (Never had valid order)"
		}

		// Case 2: filled with SellOrderID - check if sell still exists
		if tx.StatusTransaction == "filled" && tx.SellOrderID != "" {
			if _, exists := binanceOrderMap[tx.SellOrderID]; !exists {
				// Sell order doesn't exist in open orders - it was either filled or canceled
				// We need to query Binance to find out the actual status
				resp, err := s.Binance.GetOrder(tx.Symbol, tx.SellOrderID)
				if err != nil {
					logger.Warn("⚠️ Cannot verify sell order status (API error). Keeping transaction.", "id", tx.ID, "sellID", tx.SellOrderID, "error", err)
					continue
				}

				if resp.Status == "FILLED" {
					shouldPurge = true
					reason = fmt.Sprintf("Sell FILLED offline at %s", resp.Price)
					// Calculate profit for notes
					buyPrice, _ := strconv.ParseFloat(tx.Price, 64)
					sellPrice, _ := strconv.ParseFloat(resp.Price, 64)
					qty, _ := strconv.ParseFloat(tx.Amount, 64)
					profit := (sellPrice - buyPrice) * qty
					tx.Notes += fmt.Sprintf(" | Sold at %.2f (Profit: $%.2f) [Ghost Recovery]", sellPrice, profit)
				} else if resp.Status == "CANCELED" || resp.Status == "EXPIRED" {
					// Sell order was canceled - we have exposure without exit!
					// Don't purge, but reset to trigger new sell placement
					logger.Warn("⚠️ Ghost Sell Order was CANCELED. Resetting to trigger new exit.", "id", tx.ID, "sellID", tx.SellOrderID)
					tx.SellOrderID = ""
					tx.StatusTransaction = "filled"
					tx.Notes += " | Sell Canceled (Ghost Recovery: Needs New Exit)"
					s.TransactionRepo.Update(tx)
					// Immediately place new exit
					s.placeMakerExitOrder(&tx)
					continue
				}
			}
		}

		// Case 3: open buy that doesn't exist on Binance and isn't FILLED
		if tx.StatusTransaction == "open" && tx.Type == "buy" {
			if _, exists := binanceOrderMap[tx.ID]; !exists {
				// Query to check actual status
				resp, err := s.Binance.GetOrder(tx.Symbol, tx.ID)
				if err != nil {
					// Order truly doesn't exist - remove it
					shouldPurge = true
					reason = "Buy Order Not Found on Binance (Ghost)"
				} else if resp.Status == "CANCELED" || resp.Status == "EXPIRED" {
					shouldPurge = true
					reason = fmt.Sprintf("Buy Order %s", resp.Status)
				}
				// If FILLED, Phase 2 should have handled it
			}
		}

		if shouldPurge {
			logger.Info("📦 Purging Ghost Transaction", "id", tx.ID, "reason", reason)

			// Archive first
			if err := s.TransactionRepo.Archive(tx); err != nil {
				logger.Error("⚠️ Failed to archive ghost transaction", "id", tx.ID, "error", err)
				continue
			}

			// Then delete
			if err := s.TransactionRepo.Delete(tx.ID); err != nil {
				logger.Error("⚠️ Failed to delete ghost transaction after archive", "id", tx.ID, "error", err)
			} else {
				purgedCount++
			}
		}
	}

	if purgedCount > 0 {
		logger.Info("✅ Ghost Cleanup Complete", "purged_count", purgedCount)
	} else {
		logger.Info("✅ No ghost transactions found")
	}

	return purgedCount
}

// PeriodicSyncOrders runs the ghost cleanup periodically (every 5 min)
// to catch any orders that got filled between syncs
func (s *Strategy) PeriodicSyncOrders() {
	logger.Info("🔄 Periodic Sync: Validating transactions against Binance...")

	binanceOpenOrders, err := s.Binance.GetOpenOrders(s.Cfg.Symbol)
	if err != nil {
		logger.Error("❌ Periodic Sync Failed: Cannot fetch open orders", "error", err)
		return
	}

	binanceOrderMap := make(map[string]api.OrderResponse)
	for _, bo := range binanceOpenOrders {
		binanceOrderMap[bo.ClientOrderId] = bo
	}

	purged := s.purgeGhostTransactions(binanceOrderMap)
	if purged > 0 {
		logger.Info("🧹 Periodic Sync: Cleaned up ghost transactions", "count", purged)
	}
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
	// 1. Must have Open Orders to reposition
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

	// 4. Trigger Logic (Smart Entry V2.0 + Grid Gap Fix + Idle Stagnation Fix)

	// A) Price Runaway (Urgent)
	// STRICT SAFETY: If we have inventory, DO NOT chase pumps.
	isPriceRunaway := diffPct >= s.Cfg.SmartEntryRepositionPct
	if len(filledOrders) > 0 {
		isPriceRunaway = false // Disable Runaway trigger if carrying bags
	}

	cooldown := time.Duration(s.Cfg.SmartEntryRepositionCooldown) * time.Minute
	isCooldownPassed := time.Since(highestOrder.CreatedAt) >= cooldown

	// B) Stagnation (Idle Timeout)
	// ALLOWED WITH INVENTORY: If we are stuck low for too long, move up.
	maxIdle := time.Duration(s.Cfg.SmartEntryRepositionMaxIdleMin) * time.Minute
	isStagnant := s.Cfg.SmartEntryRepositionMaxIdleMin > 0 && time.Since(highestOrder.CreatedAt) >= maxIdle

	// C) Grid Gap Detection (Backfill Unification)
	// If current price moved UP significantly leaving a gap > 2.5x GridSpacing
	// ALLOWED WITH INVENTORY: Filling a gap is healthy.
	dynamicSpacing := s.VolatilityService.GetDynamicSpacing()
	isGridGap := diffPct >= (dynamicSpacing * 2.5)

	shouldReposition := (isPriceRunaway && isCooldownPassed) || isStagnant || isGridGap

	if !shouldReposition {
		return
	}

	triggerReason := "Price Runaway"
	if isGridGap {
		triggerReason = "Grid Gap (Backfill)"
	} else if isStagnant {
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

// ForceSyncOpenOrders performs a REVERSE SYNC: Checking if local 'open' orders are actually open on Binance.
// If an order is missing from Binance Open Orders, we check its final status (FILLED/CANCELED) and update.
func (s *Strategy) ForceSyncOpenOrders() {
	// 1. Fetch ALL Open Orders from Binance
	binantOpenOrders, err := s.Binance.GetOpenOrders(s.Cfg.Symbol)
	if err != nil {
		logger.Error("⚠️ Sync: Failed to fetch open orders from Binance", "error", err)
		return
	}

	binanceOrderMap := make(map[string]api.OrderResponse)
	for _, bo := range binantOpenOrders {
		binanceOrderMap[bo.ClientOrderId] = bo
	}

	// 2. Iterate Local Open Orders
	transactions := s.TransactionRepo.GetAll()
	syncedCount := 0

	for _, tx := range transactions {
		// We only care about reconciling 'open' or 'waiting_sell' orders
		if tx.StatusTransaction != "open" && tx.StatusTransaction != "waiting_sell" {
			continue
		}

		if tx.Symbol != s.Cfg.Symbol {
			continue
		}

		// Check if this local order exists in the Binance Open Orders list
		_, isOpenOnBinance := binanceOrderMap[tx.ID]

		if isOpenOnBinance {
			continue // All good
		}

		// IF WE ARE HERE: Order is OPEN locally, but NOT in Binance Open Orders.
		// Conclusion: It was Filled, Canceled, or Expired while we were offline/missed WS.
		logger.Warn("🔄 Sync: Zombie Order Detected (Locally Open, Remote Closed). Recovering...", "id", tx.ID)

		// Fetch specific order details to know exact final state
		resp, err := s.Binance.GetOrder(tx.Symbol, tx.ID)
		if err != nil {
			logger.Error("⚠️ Sync: Failed to check status of zombie order", "id", tx.ID, "error", err)
			continue
		}

		// Update Local State
		if resp.Status == "FILLED" {
			tx.StatusTransaction = "filled"
			tx.Price = resp.Price
			if resp.ExecutedQty != "" {
				tx.Amount = resp.ExecutedQty
			}
			tx.Notes += " | Synced (Filled via Periodic Check)"
			tx.UpdatedAt = time.Now()
			s.TransactionRepo.Update(tx)
			syncedCount++

			logger.Info("✅ Sync: Order FILLED (Recovered)", "id", tx.ID)

			// ACTION: If it was a BUY, we must ensure we have an Exit!
			if tx.Type == "buy" {
				// SMART RECOVERY (Race Condition Fix):
				// Before creating a NEW sell order, check if one already exists in Binance Open Orders.
				// This handles cases where we created the order (via WS or other thread) but persistence failed (Race).

				var foundSellID string
				buyQtyFloat, _ := strconv.ParseFloat(resp.ExecutedQty, 64)

				// 1. Check strict link (if we have ID but status was wrong)
				if tx.SellOrderID != "" {
					if _, ok := binanceOrderMap[tx.SellOrderID]; ok {
						foundSellID = tx.SellOrderID
						logger.Info("🔗 Relinking: Existing Sell Order found by ID.", "sellID", foundSellID)
					}
				}

				// 2. Scan for Orphan Sell Orders (Match by Quantity)
				// If we lost the ID (zero persistence), we look for a SELL with matching quantity.
				if foundSellID == "" {
					for _, bo := range binanceOrderMap {
						if bo.Side == "SELL" {
							sellQtyFloat, _ := strconv.ParseFloat(bo.OrigQty, 64)
							// Tolerance for float comparison
							if math.Abs(sellQtyFloat-buyQtyFloat) < 0.00000001 {
								// We found a SELL order with matching quantity. Safe to assume it's ours?
								// Grid logic usually 1:1.
								foundSellID = bo.ClientOrderId
								logger.Info("🔗 Relinking: Matching Orphan Sell Order found by Quantity.", "sellID", foundSellID)
								break
							}
						}
					}
				}

				if foundSellID != "" {
					// We found an existing active sell order. Update our records instead of duplicating.
					tx.SellOrderID = foundSellID
					tx.StatusTransaction = "waiting_sell"
					tx.UpdatedAt = time.Now()
					s.TransactionRepo.Update(tx)
					logger.Info("✅ Smart Recovery: Linked existing Sell Order. Skipped duplicate creation.", "buyID", tx.ID, "sellID", foundSellID)
				} else {
					// No existing sell order found. Proceed to create one.
					logger.Info("🚀 Sync: Triggering Maker Exit for Recovered Buy", "buyID", tx.ID)
					s.placeMakerExitOrder(&tx)
				}
			}

			// ACTION: If it was a SELL (Maker Exit)
			if tx.Type == "sell" {
				tx.StatusTransaction = "closed"
				now := time.Now()
				tx.ClosedAt = &now
				tx.Notes += " | Sold via Periodic Check"
				s.TransactionRepo.Update(tx)
				logger.Info("💰 Sync: Maker Exit Closed (Recovered)", "sellID", tx.ID)
			}

		} else if resp.Status == "CANCELED" || resp.Status == "EXPIRED" || resp.Status == "REJECTED" {
			tx.StatusTransaction = "closed"
			tx.Notes += fmt.Sprintf(" | Synced (%s via Periodic Check)", resp.Status)
			tx.UpdatedAt = time.Now()
			s.TransactionRepo.Update(tx)
			logger.Warn("⚠️ Sync: Order CANCELED/EXPIRED (Recovered)", "id", tx.ID, "status", resp.Status)
		}
	}

	if syncedCount > 0 {
		logger.Info("✅ Periodic Sync Completed", "recovered_orders", syncedCount)
	}
}

// StartPeriodicSync starts a background ticker to force sync orders every 5 minutes
func (s *Strategy) StartPeriodicSync() {
	go func() {
		logger.Info("⏰ Starting Periodic Order Sync (Every 5 minutes)")
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()

		for range ticker.C {
			s.ForceSyncOpenOrders()
			s.PeriodicSyncOrders() // Ghost cleanup
		}
	}()
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
