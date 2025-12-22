package service

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"grid-trading-btc-binance/internal/config"
	"grid-trading-btc-binance/internal/logger"
	"grid-trading-btc-binance/internal/model"
)

type TelegramService struct {
	Cfg *config.Config
}

func NewTelegramService(cfg *config.Config) *TelegramService {
	return &TelegramService{
		Cfg: cfg,
	}
}

func (s *TelegramService) SendMessage(text string) {
	if s.Cfg.TelegramToken == "" || s.Cfg.TelegramChatID == "" {
		logger.Warn("Telegram credentials not set, skipping message")
		return
	}

	url := fmt.Sprintf("https://api.telegram.org/bot%s/sendMessage", s.Cfg.TelegramToken)
	payload := map[string]string{
		"chat_id":    s.Cfg.TelegramChatID,
		"text":       text,
		"parse_mode": "Markdown",
	}

	jsonPayload, err := json.Marshal(payload)
	if err != nil {
		logger.Error("Failed to marshal Telegram payload", "error", err)
		return
	}

	// Send async
	go func() {
		resp, err := http.Post(url, "application/json", bytes.NewBuffer(jsonPayload))
		if err != nil {
			logger.Error("Failed to send Telegram message", "error", err)
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			logger.Error("Telegram API error", "status", resp.Status)
		}
	}()
}

func (s *TelegramService) SendTradeNotification(tx model.Transaction, profit float64, closedOrders []model.Transaction, usdtBalance, bnbBalance, btcBalance float64) {
	var msg string
	now := time.Now().Format("02/01/2006, 15:04:05")

	amount, _ := strconv.ParseFloat(tx.Amount, 64)
	price, _ := strconv.ParseFloat(tx.Price, 64)
	total := amount * price

	// Escape IDs for Markdown
	escapedTxID := s.escapeMarkdown(tx.ID)

	if tx.Type == "sell" {
		// VENDA (Taker Profit)
		var closedOrdersMsg string
		if len(closedOrders) > 0 {
			closedOrdersMsg = "\nOrdens Buy:"
			for _, order := range closedOrders {
				closedOrdersMsg += fmt.Sprintf("\n- %s", s.escapeMarkdown(order.ID))
			}
		}

		msg = fmt.Sprintf(
			"🤖 Grid Trading - %s - Binance\n"+
				"🆔 ID: %s\n"+
				"📊 Status: %s\n"+
				"🟢 Lado: VENDA\n"+
				"📦 Qtd: %.6f\n"+
				"💲 Preço: $%.2f\n"+
				"💵 Total: $%.2f\n"+
				"💰 LUCRO (Realizado): $%.4f\n"+
				"%s\n\n"+
				"💰 Saldo USDT: $%.2f\n"+
				"💰 Saldo BNB: %.4f\n"+
				"📅 Data: %s",
			tx.Symbol,
			escapedTxID,
			s.escapeMarkdown(tx.StatusTransaction),
			amount,
			price,
			total,
			profit,
			closedOrdersMsg,
			usdtBalance,
			bnbBalance,
			now,
		)
	} else {
		// COMPRA (Maker Fill)
		msg = fmt.Sprintf(
			"🤖 Grid Trading - %s - Binance\n"+
				"🆔 ID: %s\n"+
				"📊 Status: %s\n"+
				"🟢 Lado: COMPRA\n"+
				"📦 Qtd: %.6f\n"+
				"💲 Preço: $%.2f\n"+
				"💵 Total: $%.2f\n\n"+
				"💰 Saldo BTC: %.6f\n"+
				"💰 Saldo USDT: $%.2f\n"+
				"📅 Data: %s",
			tx.Symbol,
			escapedTxID,
			s.escapeMarkdown(tx.StatusTransaction),
			amount,
			price,
			total,
			btcBalance,
			usdtBalance,
			now,
		)

	}

	s.SendMessage(msg)
}

func (s *TelegramService) SendLowBalanceAlert(currency string, currentBalance, required float64) {
	now := time.Now().Format("02/01/2006, 15:04:05")
	var msg string

	if currency == "USDT" {
		msg = fmt.Sprintf(
			"⚠️ *ALERTA: Saldo USDT Baixo*\n\n"+
				"💰 Saldo Atual: $%.2f\n"+
				"📉 Necessário: $%.2f\n"+
				"⚠️ O bot não conseguiu posicionar novas ordens de compra.\n\n"+
				"📅 %s",
			currentBalance, required, now,
		)
	} else {
		msg = fmt.Sprintf(
			"⚠️ *ALERTA: Saldo BNB Baixo*\n\n"+
				"💰 Saldo BNB: %.4f\n"+
				"📉 Limite Aproximado: %.4f\n"+
				"⚠️ O saldo BNB está baixo para taxas (menos de 5%% do valor da ordem). Considere recarregar.\n\n"+
				"📅 %s",
			currentBalance, required, now,
		)
	}
	s.SendMessage(msg)
}

func (s *TelegramService) escapeMarkdown(text string) string {
	// Replace _ with \_ to prevent Markdown parsing errors
	// In Go strings.ReplaceAll, backslash needs to be escaped too
	return strings.ReplaceAll(text, "_", "\\_")
}
