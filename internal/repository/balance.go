package repository

import (
	"fmt"
	"grid-trading-btc-binance/internal/logger"
	"grid-trading-btc-binance/internal/model"
)

const balancesFile = "balances.json"

type BalanceRepository struct {
	storage *Storage
	cache   map[string]*model.Balance
}

func NewBalanceRepository(storage *Storage) *BalanceRepository {
	return &BalanceRepository{
		storage: storage,
		cache:   make(map[string]*model.Balance),
	}
}

func (r *BalanceRepository) Load() error {
	if !r.storage.Exists(balancesFile) {
		logger.Info("balances.json not found, initializing with defaults")
		return r.initDefaults()
	}

	var balances []model.Balance
	if err := r.storage.Read(balancesFile, &balances); err != nil {
		return err
	}

	for i := range balances {
		r.cache[balances[i].Currency] = &balances[i]
	}
	return nil
}

func (r *BalanceRepository) initDefaults() error {
	defaults := []model.Balance{
		{Currency: "BNB", Amount: 0.022611329296234092},
		{Currency: "BTC", Amount: 0},
		{Currency: "USDT", Amount: 37.26492662018087},
	}

	if err := r.storage.Write(balancesFile, defaults); err != nil {
		return fmt.Errorf("failed to write default balances: %w", err)
	}

	for i := range defaults {
		r.cache[defaults[i].Currency] = &defaults[i]
	}
	return nil
}

func (r *BalanceRepository) Get(currency string) (*model.Balance, bool) {
	b, ok := r.cache[currency]
	return b, ok
}

func (r *BalanceRepository) Update(currency string, amount float64) error {
	if b, ok := r.cache[currency]; ok {
		b.Amount = amount
	} else {
		r.cache[currency] = &model.Balance{Currency: currency, Amount: amount}
	}
	return r.save()
}

func (r *BalanceRepository) save() error {
	var balances []model.Balance
	for _, b := range r.cache {
		balances = append(balances, *b)
	}
	return r.storage.Write(balancesFile, balances)
}
