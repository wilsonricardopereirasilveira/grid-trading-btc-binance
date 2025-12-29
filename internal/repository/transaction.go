package repository

import (
	"fmt"
	"grid-trading-btc-binance/internal/logger"
	"grid-trading-btc-binance/internal/model"
	"sync"
	"time"
)

const transactionsFile = "transactions.json"

type TransactionRepository struct {
	storage      *Storage
	transactions []model.Transaction
	mu           sync.RWMutex
}

func NewTransactionRepository(storage *Storage) *TransactionRepository {
	return &TransactionRepository{
		storage:      storage,
		transactions: []model.Transaction{},
	}
}

func (r *TransactionRepository) Load() error {
	r.mu.Lock()
	defer r.mu.Unlock()

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
	r.mu.Lock()
	defer r.mu.Unlock()

	r.transactions = append(r.transactions, tx)
	return r.storage.Write(transactionsFile, r.transactions)
}

func (r *TransactionRepository) Update(tx model.Transaction) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i, t := range r.transactions {
		if t.ID == tx.ID {
			r.transactions[i] = tx
			return r.storage.Write(transactionsFile, r.transactions)
		}
	}
	return fmt.Errorf("transaction not found: %s", tx.ID)
}

func (r *TransactionRepository) Get(id string) (model.Transaction, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, tx := range r.transactions {
		if tx.ID == id {
			return tx, true
		}
	}
	return model.Transaction{}, false
}

func (r *TransactionRepository) GetBySellID(sellID string) (model.Transaction, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	for _, tx := range r.transactions {
		if tx.SellOrderID == sellID {
			return tx, true
		}
	}
	return model.Transaction{}, false
}

func (r *TransactionRepository) GetAll() []model.Transaction {
	r.mu.RLock()
	defer r.mu.RUnlock()

	// Return a copy to prevent race conditions on slice elements if modified outside
	// However, simple copy is shallow. Since Transaction struct has no pointers (Wait.. ClosedAt is *time.Time),
	// we should be careful. But for reading, value receiver copy is usually enough.
	// `transactions` slice contains values, so `return r.transactions` returns a slice COPY,
	// but referencing the SAME underlying array. This IS NOT thread safe if caller append/modifies.
	// We should return a true copy.
	copied := make([]model.Transaction, len(r.transactions))
	copy(copied, r.transactions)
	return copied
}

func (r *TransactionRepository) GetTransactionsAfter(timestamp time.Time) []model.Transaction {
	r.mu.RLock()
	defer r.mu.RUnlock()

	var filtered []model.Transaction
	for _, tx := range r.transactions {
		if tx.StatusTransaction == "filled" || tx.StatusTransaction == "closed" {
			if tx.UpdatedAt.After(timestamp) {
				filtered = append(filtered, tx)
			}
		}
	}
	return filtered
}

// GetClosedTransactionsAfter reads the history file and returns closed transactions after timestamp
// Used by the collector to calculate hourly realized profits from archived trades
func (r *TransactionRepository) GetClosedTransactionsAfter(timestamp time.Time) []model.Transaction {
	historyFile := "logs/transactions_history.json"

	var history []model.Transaction
	if !r.storage.Exists(historyFile) {
		return history
	}

	if err := r.storage.Read(historyFile, &history); err != nil {
		logger.Error("Failed to read history for metrics", "error", err)
		return history
	}

	var filtered []model.Transaction
	for _, tx := range history {
		if tx.StatusTransaction == "closed" {
			// For closed trades, use ClosedAt if available, else UpdatedAt
			var checkTime time.Time
			if tx.ClosedAt != nil {
				checkTime = *tx.ClosedAt
			} else {
				checkTime = tx.UpdatedAt
			}

			if checkTime.After(timestamp) {
				filtered = append(filtered, tx)
			}
		}
	}
	return filtered
}

// Remove deletes a transaction by ID and saves to file
func (r *TransactionRepository) Remove(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	for i, tx := range r.transactions {
		if tx.ID == id {
			r.transactions = append(r.transactions[:i], r.transactions[i+1:]...)
			return r.storage.Write(transactionsFile, r.transactions)
		}
	}
	return nil
}

// Clear removes all transactions and saves empty list
func (r *TransactionRepository) Clear() error {
	r.mu.Lock()
	defer r.mu.Unlock()

	r.transactions = []model.Transaction{}
	return r.storage.Write(transactionsFile, r.transactions)
}

// Archive appends a closed transaction to the history file
func (r *TransactionRepository) Archive(tx model.Transaction) error {
	historyFile := "logs/transactions_history.json"

	// We need to read existing history first to append
	// Optimization: This might be slow if history gets huge.
	// Ideally we would append to a file stream, but JSON structure requires reading the array.
	// For "simple" archiving where we just want to save the record, appending to a JSON array is standard but costly.
	// Alternative: JSON Lines (NDJSON). But user asked for .json.
	// We will follow the pattern: Read -> Append -> Write.

	var history []model.Transaction
	if r.storage.Exists(historyFile) {
		if err := r.storage.Read(historyFile, &history); err != nil {
			logger.Error("Failed to read history file during archive", "error", err)
			// Proceed with empty history or return error?
			// Let's try to proceed to avoid losing data if read fails due to corruption?
			// Safety: Return error to prompt manual check.
			return err
		}
	} else {
		// Ensure logs dir exists? Storage might handle it if path contains separator.
		// Assuming Storage abstraction handles it or directory exists.
	}

	history = append(history, tx)
	return r.storage.Write(historyFile, history)
}

// Delete removes a transaction by ID from memory and saves the active file
func (r *TransactionRepository) Delete(id string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	originalLen := len(r.transactions)
	var newTransactions []model.Transaction
	for _, t := range r.transactions {
		if t.ID != id {
			newTransactions = append(newTransactions, t)
		}
	}

	if len(newTransactions) == originalLen {
		return fmt.Errorf("transaction not found for deletion: %s", id)
	}

	r.transactions = newTransactions
	return r.storage.Write(transactionsFile, r.transactions)
}

// CleanupClosed iterates through loaded transactions, archives closed ones, and removes them from active list.
// Should be called at startup.
func (r *TransactionRepository) CleanupClosed() int {
	r.mu.Lock()
	defer r.mu.Unlock()

	var activeTransactions []model.Transaction
	var closedCount int

	// Iterate and filter
	// We cannot call r.Archive() here because it locks. We must implement archive logic inline or unlock/lock.
	// Or better: Collect closed ones, then archive them in bulk, then save active.

	var closedTransactions []model.Transaction

	for _, tx := range r.transactions {
		if tx.StatusTransaction == "closed" {
			closedTransactions = append(closedTransactions, tx)
			closedCount++
		} else {
			activeTransactions = append(activeTransactions, tx)
		}
	}

	if closedCount == 0 {
		return 0
	}

	logger.Info("🧹 Cleanup: Found closed transactions to archive", "count", closedCount)

	// Archive Logic (Bulk)
	historyFile := "logs/transactions_history.json"

	// Read History (Needs to be outside Lock if storage.Read takes time? No, we are holding lock for consistency)
	// Be careful with performance. Reading giant history file while holding lock on active transactions might block bot.
	// But this is Startup routine, so blocking is acceptable.

	var history []model.Transaction
	if r.storage.Exists(historyFile) {
		if err := r.storage.Read(historyFile, &history); err != nil {
			logger.Error("❌ Cleanup Failed: Could not read history file", "error", err)
			return 0 // Abort to keep data safe in active list
		}
	}

	history = append(history, closedTransactions...)

	if err := r.storage.Write(historyFile, history); err != nil {
		logger.Error("❌ Cleanup Failed: Could not write history file", "error", err)
		return 0 // Abort
	}

	// Update Active
	r.transactions = activeTransactions
	if err := r.storage.Write(transactionsFile, r.transactions); err != nil {
		logger.Error("❌ Cleanup Failed: Could not write active file", "error", err)
		// Danger state: History updated but Active not cleared. transactions duplicates in history?
		// Acceptable risk for now vs complexity.
		return 0
	}

	logger.Info("✅ Cleanup Complete: Archived and Removed transactions", "count", closedCount)
	return closedCount
}
