# Changelog

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
