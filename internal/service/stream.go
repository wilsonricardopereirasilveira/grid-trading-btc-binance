package service

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/gorilla/websocket"

	"grid-trading-btc-binance/internal/api"
	"grid-trading-btc-binance/internal/logger"
)

const (
	StreamBaseURL = "wss://stream.binance.com:9443/ws"
)

// OrderUpdate represents the payload from executionReport event
type OrderUpdate struct {
	Event         string `json:"e"` // Event type
	EventTime     int64  `json:"E"`
	Symbol        string `json:"s"`
	ClientOrderID string `json:"c"`
	Side          string `json:"S"`
	Type          string `json:"o"`
	TimeInForce   string `json:"f"`
	Quantity      string `json:"q"`
	Price         string `json:"p"`
	StopPrice     string `json:"P"`
	IcebergQty    string `json:"F"`
	OrderListId   int64  `json:"g"` // -1 for ordinary orders
	OriginalID    string `json:"C"` // Original client order ID
	ExecutionType string `json:"x"` // Current execution type (NEW, CANCELED, REPLACED, REJECTED, TRADE, EXPIRED)
	Status        string `json:"X"` // Current order status (NEW, PARTIALLY_FILLED, FILLED, CANCELED, PENDING_CANCEL, REJECTED, EXPIRED)
	RejectReason  string `json:"r"`
	OrderID       int64  `json:"i"` // Order ID
	LastExecQty   string `json:"l"` // Last executed quantity
	CumExecQty    string `json:"z"` // Cumulative executed quantity
	LastExecPrice string `json:"L"` // Last executed price
	Commission    string `json:"n"` // Commission amount
	CommAsset     string `json:"N"` // Commission asset
	TxTime        int64  `json:"T"` // Transaction time
	TradeID       int64  `json:"t"` // Trade ID
	Ignore        int64  `json:"I"` // Ignore
	IsWorking     bool   `json:"w"` // Is the order on the book?
	IsMaker       bool   `json:"m"` // Is this trade the maker side?
	OrderCreation int64  `json:"O"` // Order creation time
	CumQuoteQty   string `json:"Z"` // Cumulative quote asset transacted quantity
	LastQuoteQty  string `json:"Y"` // Last quote asset transacted quantity (e.g. USDT)
	QuoteOrderQty string `json:"Q"` // Quote Order Qty
	WorkingTime   int64  `json:"W"` // Working Time
	SelfTradePrev string `json:"V"` // SelfTradePreventionMode
}

type StreamService struct {
	Binance     *api.BinanceClient
	ListenKey   string
	WSConn      *websocket.Conn
	Updates     chan OrderUpdate
	StopCh      chan struct{}
	IsConnected bool
}

func NewStreamService(binance *api.BinanceClient) *StreamService {
	return &StreamService{
		Binance: binance,
		Updates: make(chan OrderUpdate, 100),
		// StopCh initialized in Start()
	}
}

func (s *StreamService) Start() error {
	// 1. Get Listen Key
	key, err := s.Binance.StartUserStream()
	if err != nil {
		return fmt.Errorf("failed to get listen key: %w", err)
	}
	s.ListenKey = key
	logger.Info("🔑 ListenKey acquired", "key", key)

	// 2. Connect to WebSocket
	url := fmt.Sprintf("%s/%s", StreamBaseURL, s.ListenKey)
	c, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		return fmt.Errorf("failed to connect to websocket: %w", err)
	}
	s.WSConn = c
	s.IsConnected = true
	logger.Info("📡 WebSocket Connected to Binance User Stream")

	// 3. Start KeepAlive Loop (30m)
	s.StopCh = make(chan struct{}) // Reset stop channel for new connection
	go s.keepAliveLoop()

	// 4. Start Reading Loop (Blocking)
	// This will block until connection is closed or Stop() is called
	s.readLoop()

	return nil
}

func (s *StreamService) keepAliveLoop() {
	ticker := time.NewTicker(30 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-s.StopCh:
			return
		case <-ticker.C:
			if err := s.Binance.KeepAliveUserStream(s.ListenKey); err != nil {
				logger.Error("❌ Failed to keep alive listen key", "error", err)
			} else {
				logger.Debug("💓 ListenKey KeepAlive sent")
			}
		}
	}
}

func (s *StreamService) readLoop() {
	defer func() {
		if s.WSConn != nil {
			s.WSConn.Close()
		}
		s.IsConnected = false
		logger.Warn("🔌 WebSocket Connection Closed")
	}()

	for {
		select {
		case <-s.StopCh:
			return
		default:
			_, message, err := s.WSConn.ReadMessage()
			if err != nil {
				logger.Error("❌ WebSocket Read Error", "error", err)
				// Reconnection logic could go here. For now, we return (stop).
				// Simple production bot might panic/exit to let supervisor restart, or retry.
				// Let's implement a simple retry or just exit loop.
				// For this task, we log and exit, Main will assume connection is critical.
				return
			}

			// Parse generic to check event type first? Or just try unmarshal.
			// The stream sends different events (outboundAccountPosition, executionReport).
			// We care about executionReport.

			// Optimistic unmarshal into OrderUpdate
			// This works because OrderUpdate contains the 'e' (Event) field.
			// Extra fields in other event types (like 'B' in outboundAccountPosition) will just be ignored
			// comfortably without error, as long as common fields (like 'e', 'E') have compatible types.
			var event OrderUpdate
			if err := json.Unmarshal(message, &event); err != nil {
				logger.Error("❌ Failed to parse WebSocket message", "error", err, "msg", string(message))
				continue
			}

			if event.Event == "executionReport" {
				s.Updates <- event
			} else if event.Event == "outboundAccountPosition" {
				// Handle balance updates if we wanted real-time balance
				// logger.Debug("Balance Update Streamed")
			}
		}
	}
}

func (s *StreamService) Stop() error {
	logger.Info("🛑 Stopping Stream Service...")
	close(s.StopCh)
	if s.ListenKey != "" {
		_ = s.Binance.CloseUserStream(s.ListenKey)
	}
	if s.WSConn != nil {
		return s.WSConn.Close()
	}
	return nil
}
