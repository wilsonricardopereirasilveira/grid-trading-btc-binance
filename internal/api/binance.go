package api

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"grid-trading-btc-binance/internal/logger"
	"grid-trading-btc-binance/internal/model"
)

const (
	BaseURL = "https://api.binance.com"
)

type BinanceClient struct {
	APIKey     string
	SecretKey  string
	BaseURL    string
	Client     *http.Client
	TimeOffset int64
}

type AccountInfoResponse struct {
	MakerCommission  int               `json:"makerCommission"`
	TakerCommission  int               `json:"takerCommission"`
	BuyerCommission  int               `json:"buyerCommission"`
	SellerCommission int               `json:"sellerCommission"`
	CanTrade         bool              `json:"canTrade"`
	CanWithdraw      bool              `json:"canWithdraw"`
	CanDeposit       bool              `json:"canDeposit"`
	UpdateTime       int64             `json:"updateTime"`
	AccountType      string            `json:"accountType"`
	Balances         []BalanceResponse `json:"balances"`
}

type BalanceResponse struct {
	Asset  string `json:"asset"`
	Free   string `json:"free"`
	Locked string `json:"locked"`
}

func NewBinanceClient(apiKey, secretKey string) *BinanceClient {
	return &BinanceClient{
		APIKey:    apiKey,
		SecretKey: secretKey,
		BaseURL:   BaseURL,
		Client:    &http.Client{Timeout: 10 * time.Second},
	}
}

// SyncTime synchronizes the local time with Binance server time
func (c *BinanceClient) SyncTime() error {
	endpoint := "/api/v3/time"
	reqURL := fmt.Sprintf("%s%s", c.BaseURL, endpoint)

	resp, err := c.Client.Get(reqURL)
	if err != nil {
		return fmt.Errorf("failed to get server time: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read time response: %w", err)
	}

	var timeResp struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := json.Unmarshal(body, &timeResp); err != nil {
		return fmt.Errorf("failed to parse time response: %w", err)
	}

	localTime := time.Now().UnixMilli()
	c.TimeOffset = timeResp.ServerTime - localTime

	logger.Info("⏰ Time Synchronized", "server_time", timeResp.ServerTime, "local_time", localTime, "offset_ms", c.TimeOffset)
	return nil
}

// serverTime returns the current time adjusted by the offset
// We subtract 1000ms as a safety bias to ensure we are slightly "behind" the server.
// Binance rejects requests > 1000ms ahead, but accepts requests up to recvWindow (60s) behind.
func (c *BinanceClient) serverTime() int64 {
	return time.Now().UnixMilli() + c.TimeOffset - 1000
}

func (c *BinanceClient) GetAccountInfo() (*AccountInfoResponse, error) {
	endpoint := "/api/v3/account"

	// Prepare parameters
	params := url.Values{}
	params.Add("omitZeroBalances", "true")
	params.Add("timestamp", strconv.FormatInt(c.serverTime(), 10))
	params.Add("recvWindow", "60000")

	// Sign request
	signature := c.sign(params.Encode())
	params.Add("signature", signature)

	// Build URL
	reqURL := fmt.Sprintf("%s%s?%s", c.BaseURL, endpoint, params.Encode())

	// Create request
	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	req.Header.Add("X-MBX-APIKEY", c.APIKey)

	// Execute request
	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("failed to execute request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		logger.Error("Binance API Error", "status", resp.Status, "body", string(body))
		return nil, fmt.Errorf("binance api returned status: %d", resp.StatusCode)
	}

	var accountInfo AccountInfoResponse
	if err := json.Unmarshal(body, &accountInfo); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response: %w", err)
	}

	return &accountInfo, nil
}

func (c *BinanceClient) sign(queryString string) string {
	mac := hmac.New(sha256.New, []byte(c.SecretKey))
	mac.Write([]byte(queryString))
	return hex.EncodeToString(mac.Sum(nil))
}

type OrderRequest struct {
	Symbol           string
	Side             string
	Type             string
	TimeInForce      string
	Quantity         string
	Price            string
	NewClientOrderID string
}

type OrderResponse struct {
	Symbol              string `json:"symbol"`
	OrderId             int64  `json:"orderId"`
	ClientOrderId       string `json:"clientOrderId"`
	TransactTime        int64  `json:"transactTime"`
	Price               string `json:"price"`
	OrigQty             string `json:"origQty"`
	ExecutedQty         string `json:"executedQty"`
	CummulativeQuoteQty string `json:"cummulativeQuoteQty"`
	Status              string `json:"status"`
	Type                string `json:"type"`
	Side                string `json:"side"`
	Fills               []struct {
		Price           string `json:"price"`
		Qty             string `json:"qty"`
		Commission      string `json:"commission"`
		CommissionAsset string `json:"commissionAsset"`
	} `json:"fills"`
}

func (c *BinanceClient) CreateOrder(req OrderRequest) (*OrderResponse, error) {
	endpoint := "/api/v3/order"

	params := url.Values{}
	params.Add("symbol", req.Symbol)
	params.Add("side", req.Side)
	params.Add("type", req.Type)
	params.Add("newOrderRespType", "FULL")

	if req.TimeInForce != "" {
		params.Add("timeInForce", req.TimeInForce)
	}
	if req.Quantity != "" {
		params.Add("quantity", req.Quantity)
	}
	if req.Price != "" {
		params.Add("price", req.Price)
	}
	if req.NewClientOrderID != "" {
		params.Add("newClientOrderId", req.NewClientOrderID)
	}

	params.Add("timestamp", strconv.FormatInt(c.serverTime(), 10))
	params.Add("recvWindow", "60000")

	// Sign
	signature := c.sign(params.Encode())
	params.Add("signature", signature)

	// POST requests parameters can be allowed in query string or body.
	// For Binance, sending as query string in body or url implies 'application/x-www-form-urlencoded'.
	// Simplest is often putting everything in the query string or encoded body.

	// Let's use form-data body
	reqURL := fmt.Sprintf("%s%s", c.BaseURL, endpoint)

	// While we can put params in URL, usually POST uses body.
	// But Binance docs say: "parameters may be sent as a query string or in the request body".
	// Let's put in the body for POST.

	r, err := http.NewRequest("POST", reqURL, nil)
	if err != nil {
		return nil, err
	}

	r.URL.RawQuery = params.Encode() // Putting in query string is often robust for signatures matching.
	// If we put in body, ensure Content-Type application/x-www-form-urlencoded.
	// Safer/Simpler with Go http client and signature: put in QueryString as we signed the query string.
	// If we put in body, we must sign the body content.

	r.Header.Add("X-MBX-APIKEY", c.APIKey)

	resp, err := c.Client.Do(r)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		logger.Error("Binance Order Error", "status", resp.Status, "body", string(body))
		return nil, fmt.Errorf("api error: %s", string(body))
	}

	var orderResp OrderResponse
	if err := json.Unmarshal(body, &orderResp); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}

	return &orderResp, nil
}

func (c *BinanceClient) GetOrder(symbol, clientOrderID string) (*OrderResponse, error) {
	endpoint := "/api/v3/order"
	params := url.Values{}
	params.Add("symbol", symbol)
	params.Add("origClientOrderId", clientOrderID)
	params.Add("timestamp", strconv.FormatInt(c.serverTime(), 10))
	params.Add("recvWindow", "60000")

	signature := c.sign(params.Encode())
	params.Add("signature", signature)

	reqURL := fmt.Sprintf("%s%s?%s", c.BaseURL, endpoint, params.Encode())

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("X-MBX-APIKEY", c.APIKey)

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read error: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		// Log but also return error containing body
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var order OrderResponse
	if err := json.Unmarshal(body, &order); err != nil {
		return nil, fmt.Errorf("unmarshal error: %w", err)
	}
	return &order, nil
}

func (c *BinanceClient) CancelOrder(symbol, clientOrderID string) (*OrderResponse, error) {
	endpoint := "/api/v3/order"
	params := url.Values{}
	params.Add("symbol", symbol)
	params.Add("origClientOrderId", clientOrderID)
	params.Add("timestamp", strconv.FormatInt(c.serverTime(), 10))
	params.Add("recvWindow", "60000")

	signature := c.sign(params.Encode())
	params.Add("signature", signature)

	reqURL := fmt.Sprintf("%s%s", c.BaseURL, endpoint)

	r, err := http.NewRequest("DELETE", reqURL, nil)
	if err != nil {
		return nil, err
	}
	r.URL.RawQuery = params.Encode()
	r.Header.Add("X-MBX-APIKEY", c.APIKey)

	resp, err := c.Client.Do(r)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read error: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var order OrderResponse
	if err := json.Unmarshal(body, &order); err != nil {
		return nil, fmt.Errorf("unmarshal error: %w", err)
	}
	return &order, nil
}

func (c *BinanceClient) GetOpenOrders(symbol string) ([]OrderResponse, error) {
	endpoint := "/api/v3/openOrders"
	params := url.Values{}
	params.Add("symbol", symbol)
	params.Add("timestamp", strconv.FormatInt(c.serverTime(), 10))
	params.Add("recvWindow", "60000")

	signature := c.sign(params.Encode())
	params.Add("signature", signature)

	reqURL := fmt.Sprintf("%s%s?%s", c.BaseURL, endpoint, params.Encode())

	req, err := http.NewRequest("GET", reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Add("X-MBX-APIKEY", c.APIKey)

	resp, err := c.Client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	// Log Weight Usage if present
	weight := resp.Header.Get("X-MBX-USED-WEIGHT-1M")
	if weight != "" {
		// Log occasionally or debug
		logger.Debug("🔥 Binance API Weight", "used_1m", weight)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read error: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var orders []OrderResponse
	if err := json.Unmarshal(body, &orders); err != nil {
		return nil, fmt.Errorf("unmarshal error: %w", err)
	}
	return orders, nil
}

type ListenKeyResponse struct {
	ListenKey string `json:"listenKey"`
}

func (c *BinanceClient) StartUserStream() (string, error) {
	endpoint := "/api/v3/userDataStream"
	reqURL := fmt.Sprintf("%s%s", c.BaseURL, endpoint)

	req, err := http.NewRequest("POST", reqURL, nil)
	if err != nil {
		return "", err
	}
	req.Header.Add("X-MBX-APIKEY", c.APIKey)

	resp, err := c.Client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	var respKey ListenKeyResponse
	if err := json.Unmarshal(body, &respKey); err != nil {
		return "", err
	}

	return respKey.ListenKey, nil
}

func (c *BinanceClient) KeepAliveUserStream(listenKey string) error {
	endpoint := "/api/v3/userDataStream"
	reqURL := fmt.Sprintf("%s%s", c.BaseURL, endpoint)

	// PUT request with listenKey
	params := url.Values{}
	params.Add("listenKey", listenKey)

	req, err := http.NewRequest("PUT", reqURL, nil)
	if err != nil {
		return err
	}
	req.URL.RawQuery = params.Encode()
	req.Header.Add("X-MBX-APIKEY", c.APIKey)

	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

func (c *BinanceClient) CloseUserStream(listenKey string) error {
	endpoint := "/api/v3/userDataStream"
	reqURL := fmt.Sprintf("%s%s", c.BaseURL, endpoint)

	params := url.Values{}
	params.Add("listenKey", listenKey)

	req, err := http.NewRequest("DELETE", reqURL, nil)
	if err != nil {
		return err
	}
	req.URL.RawQuery = params.Encode()
	req.Header.Add("X-MBX-APIKEY", c.APIKey)

	resp, err := c.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}
	return nil
}

type BookTickerResponse struct {
	Symbol   string `json:"symbol"`
	BidPrice string `json:"bidPrice"`
	BidQty   string `json:"bidQty"`
	AskPrice string `json:"askPrice"`
	AskQty   string `json:"askQty"`
}

func (c *BinanceClient) GetBookTicker(symbol string) (*BookTickerResponse, error) {
	endpoint := "/api/v3/ticker/bookTicker"
	reqURL := fmt.Sprintf("%s%s?symbol=%s", c.BaseURL, endpoint, symbol)

	resp, err := c.Client.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read error: %w", err)
	}

	var ticker BookTickerResponse
	if err := json.Unmarshal(body, &ticker); err != nil {
		return nil, fmt.Errorf("unmarshal error: %w", err)
	}
	return &ticker, nil
}

func (c *BinanceClient) GetExchangeInfo(symbol string) (*model.ExchangeInfoResponse, error) {
	endpoint := "/api/v3/exchangeInfo"
	// If symbol is provided, we can filter for efficiency
	reqURL := fmt.Sprintf("%s%s", c.BaseURL, endpoint)
	if symbol != "" {
		reqURL = fmt.Sprintf("%s?symbol=%s", reqURL, symbol)
	}

	resp, err := c.Client.Get(reqURL)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("status %d: %s", resp.StatusCode, string(body))
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read error: %w", err)
	}

	var info model.ExchangeInfoResponse
	if err := json.Unmarshal(body, &info); err != nil {
		return nil, fmt.Errorf("unmarshal error: %w", err)
	}
	return &info, nil
}
