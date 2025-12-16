package repository

import (
	"grid-trading-btc-binance/internal/model"
	"sync"
)

type BalanceRepository struct {
	cache map[string]*model.Balance
	mu    sync.RWMutex
}

func NewBalanceRepository() *BalanceRepository {
	return &BalanceRepository{
		cache: make(map[string]*model.Balance),
	}
}

// SetBalances replaces the entire balance cache with new data from API
func (r *BalanceRepository) SetBalances(balances []model.Balance) {
	r.mu.Lock()
	defer r.mu.Unlock()

	// Re-initialize map or clear it?
	// To be safe and handling strictly what API sends, we can clear.
	// But if API omits something that we have (locally), we might lose it?
	// The API returns "non-zero balances". If we had it and now it's zero, API won't send it.
	// So we should probably verify if we want to keep zero balances or not.
	// For simplicity, let's update existing and add new.
	// But if user withdrew everything, we should set to 0.
	// "omitZeroBalances=true" means we won't get 0s.
	// If we clear map, we lose 0s.
	// Let's just blindly update/insert for now.
	// If a balance becomes 0 on binance and they don't send it, our local cache will stay at old value?
	// That's a risk.
	// However, usually we care about what we HAVE.
	// Let's clear and rebuild to be 100% in sync with API (assuming API sends all relevant info).
	// If API sends only non-zero, then 0s are effectively 0.

	r.cache = make(map[string]*model.Balance)
	for i := range balances {
		r.cache[balances[i].Currency] = &balances[i]
	}
}

func (r *BalanceRepository) Get(currency string) (*model.Balance, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	b, ok := r.cache[currency]
	if !ok {
		return nil, false
	}
	// Return a copy to prevent race conditions if caller modifies it?
	// The original code returned pointer. Let's return pointer but safely.
	// Ideally return value, but rest of app expects pointer?
	// Let's return a copy of the struct, but as a pointer.
	val := *b
	return &val, true
}

func (r *BalanceRepository) Update(currency string, amount float64) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if b, ok := r.cache[currency]; ok {
		b.Amount = amount
	} else {
		r.cache[currency] = &model.Balance{Currency: currency, Amount: amount}
	}
	return nil
}
