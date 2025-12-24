# Changelog

## 2025-12-23
### Adicionado
- **SyncOrdersOnStartup (Two-Way Sync)**:
    - **O que é**: Implementação de sincronização bidirecional no startup. O bot agora consulta a API da Binance e:
        1. **Importa Ordens Órfãs**: Se houver ordens na Binance não listadas localmente (ex: criadas antes de um crash do bot), ele as importa.
        2. **Atualiza Status Offline**: Verifica se ordens "Open" locais foram Preenchidas ou Canceladas enquanto o bot estava desligado.
    - **Resultado**: Elimina o "ponto cego" onde o bot perdia o rastreio de ordens e saldo. Se encontrar uma compra preenchida offline, lança a venda imediatamente.

- **Grid Gap Detection (Smart Backfill)**:
    - **O que é**: Unificação da lógica de "Smart Entry Repositioning" com "Backfill".
    - **Como funciona**: O bot agora monitora a distância entre a maior ordem de compra e o preço atual. Se essa distância ("Gap") for maior que **2.5x** o espaçamento do grid, o bot entende que o grid ficou para trás.
    - **Ação**: Automaticamente cancela a ordem de entrada mais antiga (fundo do grid) e a reposiciona no topo (`CurrentBid`), fechando o buraco e acompanhando a subida do mercado sem aumentar a exposição de capital.


## 2025-12-22
### Corrigido (Hotfix)
- **Correção de Loop de Ordens (Stop-Gap -2010)**:
    - **TickSize Discovery**: O bot agora consulta a API da Binance (`exchangeInfo`) na inicialização para descobrir o `tickSize` exato do ativo (ex: 0.01 para BTCUSDT), eliminando "adivinhações" e arredondamentos incorretos.
    - **Smart Retry Logic**: 
        - Resolução definitiva do erro `Order would immediately match and take` (-2010). 
        - Se a tentativa de ordem `MAKER` for rejeitada por estar no topo do book, o bot aplica um **backoff inteligente** (espera 200-500ms) e retenta com o preço ajustado (`Bid - TickSize`), garantindo a execução passiva sem estourar taxas Taker.
- **Correção de Dados e Relatórios (CSV)**:
    - **Data Integrity**: Removidas colunas duplicadas que quebravam o alinhamento do arquivo `analyze_strategy.csv`.
    - **Maker Sales Reporting**: Ajustada a lógica do coletor para reconhecer transações `closed` como vendas realizadas (adaptando-se à estratégia Maker-Maker que não gera nova tx de venda).
    - **PnL Real**: O lucro realizado agora é calculado matematicamente (`(SellPrice - BuyPrice) * Quantity`) garantindo precisão financeira nos relatórios horários.

## 2025-12-21
### Adicionado
- **Maker-Maker Strategy (Full Refactor)**:
    - **Execução Passiva Total**: Mudança fundamental na estratégia. Agora, cada ordem de compra (`Maker Entry`) gera **imediatamente** uma ordem de venda correspondente (`Maker Exit`) no book, eliminando a dependência de polling e garantindo taxas Maker (0.075%/0.1%) nas duas pontas.
    - **Zero Latency Exit**: A ordem de venda é posicionada no mesmo milissegundo em que a compra é confirmada via WebSocket, garantindo que o bot nunca fique exposto ao mercado sem um alvo de saída definido.
    - **Event Driven Architecture**: Remoção completa do loop de polling (`checkTakeProfit`). O bot agora reage 100% a eventos de WebSocket (`executionReport`), reduzindo uso de CPU e chamadas de API desnecessárias.
    - **Segurança & Resiliência**:
        - **Idempotência**: Proteção contra duplicidade de ordens caso o WebSocket envie o mesmo evento duas vezes.
        - **Fail-Safe Balance**: Verificação de saldo em tempo real com fator de segurança (0.999) antes de posicionar a venda, prevenindo erros de "Insufficient Balance" por dust.
        - **Sync Robusto**: No startup, o bot detecta se uma venda "pendente" (waiting_sell) foi executada enquanto estava offline e contabilidade o lucro corretamente.
        - **Critical Alert**: Se a ordem de venda falhar após 5 tentativas (retries com backoff), o bot marca o status como `failed_placement` e envia alerta crítico no Telegram.
- **Smart Entry V2.0 (Time-Based Reposition)**:
    - **Trigger Híbrido**: Evolução da lógica de perseguição de preço. Agora o bot reposiciona a ordem de entrada em **dois cenários**:
        - **Urgência (Price Runaway)**: Se o preço fugir X% rapidamente (setup original).
        - **Estagnação (Idle Timeout)**: Se a ordem ficar parada no book "mofando" por Y minutos (ex: 20 min), mesmo sem variação de preço, para evitar custo de oportunidade em mercados laterais.
    - **Configuração**: Adicionado `SMART_ENTRY_REPOSITION_MAX_IDLE_MIN` no `.env`.
    - **Visibilidade**: Logs diferenciados indicando a razão do reposicionamento (`Price Runaway` vs `Stagnation`).
## 2025-12-18
### Adicionado
- **Refatoração de Logging (Smart Observability)**:
    - **Throttling de Preço**: Log de "Price Update" reduzido para cada 10 segundos (exceto se houver variação > 0.5%).
    - **Monitor de Peso de API (Binance)**: Implementada lógica inteligente que avisa a cada 100 pontos de consumo ou em níveis de alerta/crítico, removendo ruído de logs DEBUG.
- **Volatility Circuit Breaker (Proteção Anti-Crash)**:
    - Mecanismo P0 que bloqueia novas compras se detectar queda brusca no mercado (ex: > 2% em 5 min).
    - **Lógica Fail-Safe**: Se a API da Binance falhar ao buscar dados, o bot assume insegurança e pausa compras.
    - **Cooldown**: Pausa automática de 15 minutos (configurável) até a estabilização.
    - **Configuração**: Novas vars `CRASH_PROTECTION_ENABLED`, `MAX_DROP_PCT_5M`, e `CRASH_PAUSE_MIN`.
- **Soft Panic Button (PAUSE_BUYS)**:
    - Nova flag configurável `PAUSE_BUYS` no `.env`.
    - Quando ativada (`true`), o bot ignora novas entradas (compras) mas mantém o gerenciamento de saídas (vendas/Take Profit), permitindo reduzir exposição sem desligar o bot.

## 2025-12-16
### Adicionado
- **Smart Entry Repositioning (Perseguição de Entrada)**:
    - Feature que reposiciona automaticamente a ordem de entrada (L1) se o mercado subir mais que X% (`SMART_ENTRY_REPOSITION_PCT`) e a ordem ficar "abandonada" por Y minutos (`SMART_ENTRY_REPOSITION_COOLDOWN_MIN`).
    - **Proteções**: 
        - **Zero Inventory Only**: Só ativa se o bot não tiver nenhuma posição em aberto (somente para entrar no mercado).
        - **Maker Priority**: A nova ordem é posicionada no `CurrentBid` para tentar execução Maker e economizar taxas.
    - **Configuração**: Adicionadas novas variáveis ao `.env`: `SMART_ENTRY_REPOSITION_PCT` e `SMART_ENTRY_REPOSITION_COOLDOWN_MIN`.


## 2025-12-15
### Melhorias
- **Análise de Estratégia (CSV)**:
    - **Agendamento**: Geração do CSV ajustada para sempre ocorrer na "hora cheia" (00min:00seg), facilitando a leitura temporal.
    - **Novas Métricas de Saúde**:
        - `unrealized_pnl_usdt`: Cálculo do PnL (Lucro/Prejuízo) Flutuante da posição em aberto (Holdings vs Preço Médio).
        - `total_fees_bnb` e `total_fees_usdt_equiv`: Monitoramento do custo operacional (Burn Rate) em taxas.
        - `open_orders_count`: Indicador de saturação do grid (0 = Ocioso, Alto = Travado).
        - `range_utilization_pct`: Medidor de risco mostrando a posição do preço dentro do Range configurado (0-100%).

## 2025-12-15
### Adicionado
- **Produção**: Estratégia migrada oficialmente de Paper Trading para Produção.
- **Dimensionamento Dinâmico de Ordens**:
    - Implementada lógica híbrida (`Max(Saldo * Pct, ValorMinimo)`).
    - Nova configuração `MIN_ORDER_VALUE` no `.env`.
- **Sistema de Alertas e Saldo**:
    - **Alerta de Saldo Insuficiente (USDT)**: Notifica no Telegram se o bot tentar abrir ordem sem saldo.
    - **Alerta de BNB Baixo**: Monitora e avisa se o saldo de BNB for menor que 5% do valor da ordem média (proteção de taxas).
    - **Throttle**: Alertas limitados a 1 envio por hora para evitar spam.
- **Logs**:
    - Logs movidos da raiz para a pasta `logs/` para melhor organização.

## 2025-12-13
### Adicionado
- **Paper Trading**: Estratégia de Grid Trading (Bitcoin/Binance) funcionando em ambiente de simulação/testes.
