package api

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"

	"grid-trading-btc-binance/internal/logger"
)

type Kline struct {
	OpenTime  int64
	Open      string
	High      string
	Low       string
	Close     string
	Volume    string
	CloseTime int64
}

func (c *BinanceClient) GetRecentKlines(symbol, interval string, limit int) ([]Kline, error) {
	endpoint := "/api/v3/klines"
	reqURL := fmt.Sprintf("%s%s", c.BaseURL, endpoint)

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}

	q := req.URL.Query()
	q.Add("symbol", symbol)
	q.Add("interval", interval)
	q.Add("limit", strconv.Itoa(limit))
	req.URL.RawQuery = q.Encode()

	// No signature needed for public data, but using API Key is good practice
	if c.APIKey != "" {
		req.Header.Add("X-MBX-APIKEY", c.APIKey)
	}

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	// Smart Logging for API Weight
	weight := resp.Header.Get("X-MBX-USED-WEIGHT-1M")
	if weight != "" {
		used, err := strconv.Atoi(weight)
		if err == nil {
			limit := 6000
			remaining := limit - used

			if used > 5400 {
				logger.Error("🚨 CRITICAL API WEIGHT", "used", used, "limit", limit, "remaining", remaining)
			} else if used > 3000 {
				logger.Warn("⚠️ High API Weight Usage", "used", used, "limit", limit, "remaining", remaining)
			} else if used%100 == 0 {
				logger.Info("📡 API Weight Monitor", "used", used, "limit", limit, "remaining", remaining)
			}
		}
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read error: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var rawKlines [][]interface{}
	if err := json.Unmarshal(body, &rawKlines); err != nil {
		return nil, fmt.Errorf("unmarshal error: %w", err)
	}

	var klines []Kline
	for _, k := range rawKlines {
		if len(k) < 7 {
			continue
		}
		// Index 0: OpenTime (float64 in json interface -> int64)
		// Index 2: High (string)
		// Index 4: Close (string)
		ot, _ := k[0].(float64) // JSON numbers are float64 by default in interface{}
		high, _ := k[2].(string)
		closePrice, _ := k[4].(string)

		klines = append(klines, Kline{
			OpenTime: int64(ot),
			High:     high,
			Close:    closePrice,
		})
	}
	return klines, nil
}
