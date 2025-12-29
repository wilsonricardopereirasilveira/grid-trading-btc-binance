# Changelog


## 2025-12-29
### Corrigido (Hotfix) - Observabilidade & Dados
- **CSV Data Integrity Fix (Profit & Fees)**:
    - **Problema**: O relatório horário `analyze_strategy.csv` apresentava colunas críticas zeradas (`realized_profit_usdt`, `total_fees_bnb`) fazendo parecer que a estratégia não lucrava.
    - **Causa Raiz**:
        1. **Profit**: O `DataCollector` lia apenas transações ativas na memória, mas transações lucrativas (fechadas) eram movidas imediatamente para o arquivo histórico (`transactions_history.json`), escapando da contabilização horária.
        2. **Fees**: A captura de taxas de comissão (`Commission`) via WebSocket não estava implementada no recebimento de eventos `executionReport`, perdendo o dado permanentemente.
    - **Solução**:
        - **Historical Lookback**: Implementado novo método `GetClosedTransactionsAfter` no Repositório. O coletor agora varre o arquivo histórico em busca de trades fechados na última hora para somar o lucro.
        - **Fee Capture**: Lógica de eventos (`HandleOrderUpdate`) atualizada para extrair e persistir as taxas tanto na compra quanto na venda, garantindo rastreio preciso de custos operacionais a partir de agora.

## 2025-12-27
### Adicionado
- **Metrics API Integration (Dashboard Sync)**:
    - **O que é**: Integração com API externa para enviar métricas do bot a cada 100 ciclos (mesmo momento do log `Cycle Metrics (Last 100)`).
    - **Payload enviado**: `strategy`, `cycles`, `min`, `max`, `avg` (em segundos), `uptime` (em segundos), `lastUpdated` e `now` (ambos em GMT-3).
    - **Configuração**: Novas vars `METRICS_API_URL` e `METRICS_API_TOKEN` no `.env` para fácil customização do endpoint e autenticação.
- **Storage & Performance Optimization (Transaction Archive)**:
    - **O que é**: Implementação de sistema de arquivamento para o `transactions.json`.
    - **Problema**: O arquivo de transações crescia indefinidamente com histórico de ordens `closed`, degradando a performance de leitura/escrita a cada ciclo.
    - **Solução**:
        - **Startup Cleanup**: Ao iniciar, o bot varre e move automaticamente transações finalizadas para `logs/transactions_history.json`.
        - **Runtime Archive**: Assim que uma venda é concluída (ciclo fechado), a transação é imediatamente arquivada e removida da memória ativa, mantendo o `transactions.json` leve contendo apenas o estado atual do grid.

### Corrigido (Critical Bug)
- **Ghost Transaction Fix (Grid Lockup)**:
    - **Problema**: Desconexões de WebSocket faziam o bot perder eventos de venda executados offline. O `transactions.json` acumulava ordens "fantasmas" (`filled` com `SellOrderID` já executado na Binance), fazendo o bot acreditar que o grid estava cheio (ex: 40/50) quando na realidade havia apenas 6 ordens reais. Isso bloqueava novas compras.
    - **Solução**:
        - **Startup Phase 3 (Ghost Cleanup)**: Ao iniciar, o bot agora valida **cada transação** contra a API da Binance. Se uma ordem não existir mais (filled/canceled offline), é arquivada e removida.
        - **Periodic Ghost Sync**: A cada 5 minutos, o bot refaz essa validação para capturar qualquer ordem preenchida entre os syncs.
        - **Smart Recovery**: Se uma ordem de venda foi cancelada (não filled), o bot automaticamente recoloca uma nova ordem de saída para proteger a exposição.
- **Duplicate Transaction Fix (Orphan Import)**:
    - **Problema**: O bot estava importando ordens de venda da Binance como "Órfãs" mesmo quando elas já estavam vinculadas a uma transação de compra local. Isso duplicava o registro (ex: 15 ordens ao invés de 7).
    - **Solução**: Ajuste na fase de Sync para ignorar IDs que já operam como `SellOrderID`. Adicionada rotina de limpeza (`Phase 4`) para remover duplicatas existentes no startup.
- **Zombie Rescue (Naked Buys Fix)**:
    - **Problema**: Foram identificadas ordens de compra "Filled" sem ordem de venda correspondente (`SellOrderID` vazio) — provável desligamento do bot logo após a compra. Isso gerava discrepância de contagem (ex: 10 locais vs 7 Binance).
    - **Solução (Phase 5)**: Nova rotina `rescueZombieTransactions` no startup.
        - Se tiver saldo: Tenta colocar a ordem de saída (Maker Exit) imediatamente.
        - Se não tiver saldo (vendido manualmente?): Arquiva a transação "zumbi" e remove da lista ativa.
- **Repositioned Order Cleanup Fix**:
    - **Problema**: Ordens reposicionadas pelo "Smart Entry Reposition" eram marcadas como `closed` mas **nunca eram arquivadas e deletadas**. Isso causava acúmulo de transações fantasmas no `transactions.json` (ex: 10 locais vs 6 Binance).
    - **Solução**: Agora, ao cancelar uma ordem para reposicionar, ela é imediatamente arquivada em `logs/transactions_history.json` e removida da lista ativa.

## 2025-12-26
### Corrigido (High Priority Bug)
- **Zero Balance / Zombie Order Fix (Race Condition)**:
    - **Problema**: Em alta volatilidade, ocorria uma condição de corrida ("Race Condition") onde o WebSocket processava uma ordem de venda, mas o loop de sincronização (`ForceSyncOpenOrders`) lia o banco de dados desatualizado e tentava vender o mesmo saldo novamente, gerando erro `Insufficient Balance` e travando a recuperação.
    - **Solução (Parte 1)**: Implementado `sync.RWMutex` no `TransactionRepository` para garantir segurança entre threads ("Thread Safety") e consistência atômica nas leituras/escritas do arquivo JSON.
    - **Solução (Parte 2)**: Lógica `Smart Recovery` aprimorada. Agora, ao detectar uma ordem "Zombie" (Aberta localmente, fechada na Binance), o bot scaneia a API em busca de ordens de venda órfãs existentes antes de tentar criar novas. Se encontrar, apenas reconecta o link no banco de dados, prevenindo duplicidade e erros de saldo.

## 2025-12-25
### Corrigido (Critical Stability)
- **Zombie Order Recovery (Auto-Heal)**:
    - **Problema**: Desconexões de WebSocket podiam deixar o bot "cego", acreditando que ordens executadas offline ainda estavam abertas, travando o grid.
    - **Solução**: Implementado `Periodic Sync` a cada 5 minutos. O bot cruza o banco local com a API da Binance, detecta discrepâncias e processa execuções perdidas (FILLED) automaticamente, recolocando a ordem oposta (Exit) imediatamente.
- **Stuck Grid Fix (IgnoreInventoryForPlacement)**:
    - **Problema**: O bot usava ordens de Venda antigas (Bags) como referência de "preço mínimo", impedindo compras durante quedas se houvesse inventário preso acima do preço atual.
    - **Solução**: Lógica `placeNewGridOrders` refatorada para ignorar vendas ao calcular o gap. Agora o bot olha apenas para **Compras Ativas**. Se não houver compras próximas, ele reinicia o grid no preço atual, permitindo operar na baixa independente do passivo.
- **Circuit Breaker & Anti-Ban (-2010 Protection)**:
    - **Problema**: Em quedas rápidas, o bot entrava em loop infinito tentando ajustar o preço, gerando erros `-2010` (Immediate Match) e risco de banimento de IP.
    - **Solução**:
        1. **Cooldown**: Se a compra falhar 3x, o bot pausa novas compras por 60 segundos.
        2. **Aggressive Backoff**: No retry, o preço é reduzido em **0.05%** (em vez de 1 tick), garantindo que a ordem entre como MAKER abaixo da faca caindo.
- **Duplicate Order Fix (Spatial Check)**:
    - **Problema**: O "Stuck Grid Fix" permitia comprar abaixo de vendas antigas, mas acabava comprando *em cima* de ordens recém-preenchidas, criando duplicatas no mesmo preço (L1/L2).
    - **Solução**: Implementada verificação espacial (`Proximity Check`). Antes de abrir compra, o bot verifica se existe **qualquer** ordem (Aberta ou Preenchida) a menos de 50% do espaçamento dinâmico. Se existir, ele bloqueia a compra para evitar "empilhamento" de capital.
- **Smart Idle Repositioning V2.0 (Reposição com Inventário)**:
    - **Problema**: Em mercados laterais ou com leve alta, o bot ficava "preso" com uma ordem de compra muito baixa e não acompanhava o preço, perdendo oportunidades de scalping, simplesmente porque tinha inventário (Bags) antigo.
    - **Solução**: Refatoração da lógica `Smart Entry`. Agora permitimos o reposicionamento da ordem de entrada **mesmo com inventário**, desde que seja por motivo de **Estagnação** (Idle Timeout de 15 min).
    - **Safety**: A proteção contra **Price Runaway** (explosão de preço) continua ativa: se o preço subir rápido demais (> 0.5%), a trava de inventário bloqueia a perseguição para evitar comprar topo.
### Melhorado
- **Observabilidade (CSV Analyst)**:
    - Adicionadas colunas profundas ao `analyze_strategy.csv`:
        - `volatility_gk`: Valor puro do estimador Garman-Klass.
        - `volatility_multiplier`: O multiplicador aplicado (1.8x ou 3.5x).
        - `dynamic_spacing_pct`: O espaçamento final usado.
        - `avg_holding_time_min`: Tempo médio de retenção dos trades.
        - `max_drawdown_pct_1h`: Estimativa de risco baseada na oscilação da última hora.
    - **Inventory Metrics Fix**:
        - **Problema**: O relatório mostrava `inventory_ratio_btc` e `unrealized_pnl_usdt` como ZERO, pois lia apenas o saldo "Livre" (Free) da carteira, ignorando que o inventário estava "Bloqueado" (Locked) em ordens de venda Limit (Maker-Maker).
        - **Solução**: `DataCollector` refatorado para calcular o inventário somando transações `filled` diretamente do banco de dados local. Agora reflete com precisão a exposição real da estratégia e o PnL flutuante das Bags.


## 2025-12-24
### Adicionado
- **Dynamic Spread via Garman-Klass (Volatilidade Avançada)**:
    - **O que é**: Implementação de espaçamento dinâmico do grid baseado na volatilidade real do mercado. Substitui o `GRID_SPACING_PCT` fixo por um cálculo matemático profissional (Garman-Klass).
    - **Regime Detection**: O bot compara a volatilidade curta (5 min) vs longa (20 min).
        - **Crash/High Vol**: Usa multiplicador 3.5x para abrir o grid e proteger capital.
        - **Normal**: Usa multiplicador 1.8x para lucrar com o ruído.
    - **Benefício**: Transforma o bot em um "Sniper" que opera agressivamente (0.1% de spread) em calma e defensivamente (2.0%+) em crashes.
    - **Configuração**: Novas vars `HIGH_VOL_MULTIPLIER` e `LOW_VOL_MULTIPLIER`.

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
