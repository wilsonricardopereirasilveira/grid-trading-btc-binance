package model

import "time"

// Transaction represents a trade execution
type Transaction struct {
	ID                string     `json:"id"`
	App               string     `json:"app"`
	Source            string     `json:"source"`
	Exchange          string     `json:"exchange"`
	TransactionID     string     `json:"transactionId"`
	Symbol            string     `json:"symbol"`
	Type              string     `json:"type"` // buy, sell, deposit, withdraw
	Amount            string     `json:"amount"`
	Price             string     `json:"price"`
	Fee               string     `json:"fee"`
	StatusTransaction string     `json:"statusTransaction"` // open, filled, cancelled, etc.
	Notes             string     `json:"notes"`
	ClosedAt          *time.Time `json:"closedAt,omitempty"`
	CreatedAt         time.Time  `json:"createdAt"`
	UpdatedAt         time.Time  `json:"updatedAt"`
}

// Balance represents the user's balance for a specific currency
type Balance struct {
	Currency string  `json:"currency"`
	Amount   float64 `json:"amount"`
}
