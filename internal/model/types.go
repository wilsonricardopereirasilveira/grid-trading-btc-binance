package model

import "time"

// Transaction represents a trade execution
type Transaction struct {
	ID                string     `json:"id"`
	TransactionID     string     `json:"transactionId"`
	Symbol            string     `json:"symbol"`
	Type              string     `json:"type"` // buy, sell, deposit, withdraw
	Amount            string     `json:"amount"`
	Price             string     `json:"price"`
	Fee               string     `json:"fee"`
	StatusTransaction string     `json:"statusTransaction"` // open, filled, cancelled, waiting_sell, closed
	Notes             string     `json:"notes"`
	ClosedAt          *time.Time `json:"closedAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`

	// Maker-Maker Strategy Fields
	SellOrderID   string    `json:"sellOrderId,omitempty"`   // ID da ordem de venda na Binance
	SellPrice     float64   `json:"sellPrice,omitempty"`     // Preço Limit da venda
	SellCreatedAt time.Time `json:"sellCreatedAt,omitempty"` // Timestamp da criação da venda
	QuantitySold  float64   `json:"quantitySold,omitempty"`  // Controle de execução parcial da venda
}

// Balance represents the user's balance for a specific currency
type Balance struct {
	Currency string  `json:"currency"`
	Amount   float64 `json:"amount"`
}
