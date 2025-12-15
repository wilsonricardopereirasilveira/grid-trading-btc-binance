package repository

import (
	"fmt"
	"grid-trading-btc-binance/internal/logger"
	"grid-trading-btc-binance/internal/model"
	"time"
)

const transactionsFile = "transactions.json"

type TransactionRepository struct {
	storage      *Storage
	transactions []model.Transaction
}

func NewTransactionRepository(storage *Storage) *TransactionRepository {
	return &TransactionRepository{
		storage:      storage,
		transactions: []model.Transaction{},
	}
}

func (r *TransactionRepository) Load() error {
	if !r.storage.Exists(transactionsFile) {
		logger.Info("transactions.json not found, creating empty")
		return r.storage.Write(transactionsFile, []model.Transaction{})
	}

	if err := r.storage.Read(transactionsFile, &r.transactions); err != nil {
		return err
	}
	return nil
}

func (r *TransactionRepository) Save(tx model.Transaction) error {
	r.transactions = append(r.transactions, tx)
	return r.storage.Write(transactionsFile, r.transactions)
}

func (r *TransactionRepository) Update(tx model.Transaction) error {
	for i, t := range r.transactions {
		if t.ID == tx.ID {
			r.transactions[i] = tx
			return r.storage.Write(transactionsFile, r.transactions)
		}
	}
	return fmt.Errorf("transaction not found: %s", tx.ID)
}

func (r *TransactionRepository) GetAll() []model.Transaction {
	return r.transactions
}

func (r *TransactionRepository) GetTransactionsAfter(timestamp time.Time) []model.Transaction {
	var filtered []model.Transaction
	for _, tx := range r.transactions {
		// Check CreatedAt or UpdatedAt depending on what "trade time" means.
		// Usually CreatedAt is fine for "when trade happened" if it's a fill.
		// But for "filled" status, we might want to check ClosedAt or UpdatedAt.
		// Let's use UpdatedAt as a proxy for "last activity" or CreatedAt if it's new.
		// The user asked for "trades_total" in the last hour.
		// A trade is a "filled" transaction.
		if tx.StatusTransaction == "filled" || tx.StatusTransaction == "closed" {
			// If it was filled/closed after timestamp
			if tx.UpdatedAt.After(timestamp) {
				filtered = append(filtered, tx)
			}
		}
	}
	return filtered
}
