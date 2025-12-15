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

		wsHandler := func(event *binance.WsAggTradeEvent) {
			price, err := strconv.ParseFloat(event.Price, 64)
			if err != nil {
				logger.Error("Failed to parse price", "symbol", symbol, "error", err)
				return
			}

			s.mu.Lock()
			s.prices[symbol] = price
			s.mu.Unlock()

			s.priceUpdates <- model.Ticker{
				Symbol: symbol,
				Price:  price,
				Time:   time.Unix(0, event.Time*int64(time.Millisecond)),
			}
		}

		errHandler := func(err error) {
			logger.Error("WebSocket error", "symbol", symbol, "error", err)
		}

		logger.Info("Connecting to Binance WS", "symbol", symbol)
		doneC, stopC, err := binance.WsAggTradeServe(symbol, wsHandler, errHandler)
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
