package model

import "time"

type Ticker struct {
	Symbol string    `json:"symbol"`
	Price  float64   `json:"price"`
	Bid    float64   `json:"bid"` // Best Bid Price
	Ask    float64   `json:"ask"` // Best Ask Price
	Time   time.Time `json:"time"`
}
