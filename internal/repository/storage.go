package repository

import (
	"encoding/json"
	"fmt"
	"os"
	"sync"
)

type Storage struct {
	mu sync.Mutex
}

func NewStorage() *Storage {
	return &Storage{}
}

func (s *Storage) Read(path string, v interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // Return nil if file doesn't exist (caller handles initialization)
		}
		return fmt.Errorf("failed to open file %s: %w", path, err)
	}
	defer file.Close()

	decoder := json.NewDecoder(file)
	if err := decoder.Decode(v); err != nil {
		return fmt.Errorf("failed to decode json from %s: %w", path, err)
	}
	return nil
}

func (s *Storage) Write(path string, v interface{}) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	file, err := os.Create(path)
	if err != nil {
		return fmt.Errorf("failed to create file %s: %w", path, err)
	}
	defer file.Close()

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	if err := encoder.Encode(v); err != nil {
		return fmt.Errorf("failed to encode json to %s: %w", path, err)
	}
	return nil
}

func (s *Storage) Exists(path string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, err := os.Stat(path)
	return !os.IsNotExist(err)
}
