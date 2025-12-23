package model

// ExchangeInfoResponse represents the response from /api/v3/exchangeInfo
type ExchangeInfoResponse struct {
	Symbols []SymbolInfo `json:"symbols"`
}

// SymbolInfo represents a single symbol's configuration
type SymbolInfo struct {
	Symbol  string   `json:"symbol"`
	Filters []Filter `json:"filters"`
}

// Filter represents a trading rule filter
type Filter struct {
	FilterType  string `json:"filterType"`
	TickSize    string `json:"tickSize,omitempty"`    // For PRICE_FILTER
	StepSize    string `json:"stepSize,omitempty"`    // For LOT_SIZE
	MinQty      string `json:"minQty,omitempty"`      // For LOT_SIZE
	MinNotional string `json:"minNotional,omitempty"` // For MIN_NOTIONAL
}
