# Grid Trading Bot (BTC/USDT) - Binance

Robô de trading autônomo de alta performance escrito em Go, focado em estratégia de Grid Trading com execução Maker-Maker na Binance.

## 🚀 Features Principais

- **Maker-Maker Strategy**: Execução passiva total (Taxas 0.075%/0.1%). Coloca a venda imediatamente ao preencher a compra (Zero Latency Exit).
- **Dynamic Grid (Garman-Klass)**: Espaçamento do grid ajusta-se automaticamente à volatilidade do mercado em tempo real.
- **Smart Entry Repositioning**: Reposiciona ordens de entrada estagnadas ou persegue o preço em tendências de alta, com proteção de cooldown.
- **Crash Protection**: Circuit Breaker que pausa compras em quedas bruscas (>2% em 5m).

## 🛡️ Segurança e Resiliência (Self-Healing)

O bot conta com um sistema robusto de recuperação de estado para garantir a integridade do capital e dos dados:

- **Transaction Archive (Performance)**:
  - Limpeza automática de ordens finalizadas (`closed`) do arquivo principal `transactions.json` para `logs/transactions_history.json`.
  - Mantém o bot leve e rápido durante execuções prolongadas.

- **Ghost Transaction Fix (Sync)**:
  - Startup Sync valida cada transação local contra a API da Binance.
  - Remove automaticamente ordens fantasmas (executadas offline) evitando travamento do grid.
  - Sincronização periódica a cada 5 minutos.

- **Zombie Rescue (Naked Buys)**:
  - Identifica compras preenchidas que ficaram sem ordem de venda (ex: queda de energia após fill).
  - Tenta criar a ordem de saída (Maker Exit) imediatamente ao reiniciar.
  - Se não houver saldo suficiente, arquiva a transação para corrigir a contabilidade.

- **Duplicate Prevention**:
  - Evita importação duplicada de ordens de venda órfãs que já pertencem a uma transação de compra.

## 🛠️ Como Executar

### Build & Run
```bash
go build -o bot.exe .
./bot.exe
```

### Linux (Nohup)
```bash
go build -o grid-bot .
chmod +x grid-bot
nohup ./grid-bot > /dev/null 2>&1 &
tail -F logs/app.log
```

## 📂 Arquitetura de Dados

- `transactions.json`: Estado atual do grid (Apenas ordens ativas/abertas).
- `logs/transactions_history.json`: Histórico completo de trades finalizados e arquivados.
- `logs/app.log`: Logs detalhados de operação.
- `logs/analyze_strategy.csv`: Métricas de performance a cada hora.
