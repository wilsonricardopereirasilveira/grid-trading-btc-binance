package service

import (
	"strconv"
	"sync"
	"time"

	"grid-trading-btc-binance/internal/logger"
	"grid-trading-btc-binance/internal/model"

	"github.com/adshao/go-binance/v2"
)

type MarketDataService struct {
	mu           sync.RWMutex
	prices       map[string]float64
	priceUpdates chan model.Ticker
	stopCh       chan struct{}
}

func NewMarketDataService() *MarketDataService {
	return &MarketDataService{
		prices:       make(map[string]float64),
		priceUpdates: make(chan model.Ticker, 100),
		stopCh:       make(chan struct{}),
	}
}

func (s *MarketDataService) Start(symbols []string) {
	for _, symbol := range symbols {
		go s.monitorSymbol(symbol)
	}
}

func (s *MarketDataService) monitorSymbol(symbol string) {
	for {
		select {
		case <-s.stopCh:
			return
		default:
			// Continue
		}

		wsHandler := func(event *binance.WsBookTickerEvent) {
			bestBid, _ := strconv.ParseFloat(event.BestBidPrice, 64)
			bestAsk, _ := strconv.ParseFloat(event.BestAskPrice, 64)
			// Use best bid as "market price" proxy or average?
			// Actually, typical implementation uses MidPrice or LastTrade.
			// BookTicker gives us Bid/Ask but not LastTrade.
			// However, for Grid, we need Bid/Ask.
			// We can use (Bid+Ask)/2 as Price, or just use Ask as reference.
			// Strategy uses `ticker.Price` for logging and checks.
			// Let's use BestAsk as "current price" to be conservative for buying?
			// Or better, let's keep AggTrade AND BookTicker?
			// No, simpler to just use BookTicker. `Price` usually implies "Last Trade Price", but "Best Bid" is more relevant for selling and "Best Ask" for buying.
			// Let's set Price = (Bid+Ask)/2 or just BestAsk.
			// Ideally we want Last Price.
			// BUT, `placeNewGridOrders` needs `currentBid`.
			// `WsBookTickerEvent` does NOT have last trade price.
			// NOTE: Switching to BookTicker means `ticker.Price` might need definition.
			// Let's define `Price` = `BestAsk` (Buying perspective) or `BestBid`?
			// Let's use `BestBid` as strict reference or Mid.
			// Actually, often Price = BestClose.
			// If we switch to BookTicker, we lose "Last Trade".
			// Is this acceptable?
			// Strategy references `ticker.Price`.
			// Let's use `BestBid` as the "Price" if we have to choose one, or Average.
			// Let's use `BestBid` to avoid premature triggering?
			// Or better: Use `WsCombinedServe` or multiple streams? Too complex.
			// Let's stick to BookTicker. `Price` = `BestBid` (Conservative).

			// Actually, to minimize impact, let's use `BestBid`.

			s.mu.Lock()
			s.prices[symbol] = bestBid
			s.mu.Unlock()

			s.priceUpdates <- model.Ticker{
				Symbol: symbol,
				Price:  bestBid, // Using Bid as reference price
				Bid:    bestBid,
				Ask:    bestAsk,
				Time:   time.Now(), // Event doesn't have standard time field always populated same way, safe to use Now
			}
		}

		errHandler := func(err error) {
			logger.Error("WebSocket error", "symbol", symbol, "error", err)
		}

		logger.Info("Connecting to Binance WS (BookTicker)", "symbol", symbol)
		doneC, stopC, err := binance.WsBookTickerServe(symbol, wsHandler, errHandler)
		if err != nil {
			logger.Error("Failed to connect to Binance WS, retrying in 5s...", "symbol", symbol, "error", err)
			time.Sleep(5 * time.Second)
			continue
		}

		// Handle stop signal or connection close
		select {
		case <-s.stopCh:
			stopC <- struct{}{}
			return
		case <-doneC:
			logger.Warn("WebSocket connection closed, reconnecting in 5s...", "symbol", symbol)
			time.Sleep(5 * time.Second)
		}
	}
}

func (s *MarketDataService) GetPrice(symbol string) (float64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	price, ok := s.prices[symbol]
	return price, ok
}

func (s *MarketDataService) GetUpdates() <-chan model.Ticker {
	return s.priceUpdates
}

func (s *MarketDataService) Stop() {
	close(s.stopCh)
}
