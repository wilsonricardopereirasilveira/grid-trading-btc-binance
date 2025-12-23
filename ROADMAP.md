# 🗺️ Roadmap de Evolução - Grid Trading Bot

## � Próximo Update (Foco Total)
**Foco**: Implementar inteligência de defesa e recuperação de capital.

- [ ] **Dynamic Spread via Garman-Klass (Volatilidade Avançada)**
  - **O que é**: Substituir o `GRID_SPACING_PCT` fixo por um cálculo dinâmico de volatilidade usando o estimador **Garman-Klass (GK)**, que é 7x mais eficiente que o ATR por considerar OHLC (Open, High, Low, Close) e Gaps.
  - **Regime Detection (Smart Multiplier)**: Comparar volatilidade curta (5 min) vs longa (20 min).
    - Se Curta > Longa * 1.5 (Aceleração/Crash): Usar `HIGH_VOL_MULTIPLIER` (ex: 3.5x) para abrir o grid.
    - Normal: Usar `LOW_VOL_MULTIPLIER` (ex: 1.8x) para lucrar no ruído.
  - **Detalhe Técnico**: Candles de 1 minuto (`interval='1m'`), pegando o bloco para cálculo.
  - **Implementação**: Polling via **REST API** a cada 60s para garantir dados estáveis (candles fechados) e baixo consumo de API.
  - **Por que**: Otimiza a entrada usando matemática financeira profissional, evitando compras prematuras no início de crashes violentos.

- [ ] **Smart Recovery Strategy (Híbrido Grid+DCA)**
  - **O que é**: Ativar modo "Resgate" em quedas profundas (ex: Nível 10+). Agrupa ordens presas e novas compras em um "pacote", calcula preço médio ponderado e sai de tudo com lucro mínimo no primeiro repique.
  - **Por que**: Evita "zombie orders" presas por meses e recicla capital rapidamente. Transforma o risco de "ficar preso no topo" em "saída pelo preço médio".

## � Backlog de Melhorias
**Status**: Aguardando priorização após o próximo update.

### Segurança e Estabilidade
- [ ] **Fila de Retry no Telegram (Resiliência)**
  - **O que é**: Implementar uma lógica simples de "tente de novo" se o envio da mensagem falhar (erro de net/timeout).
  - **Por que**: Atualmente, se a internet piscar no segundo do alerta de "Saldo Baixo", você nunca fica sabendo.

- [ ] **Organização de Arquivos**
  - **O que é**: Definir um diretório fixo (ex: /data/) para transactions.json e logs, separando código de dados.
  - **Por que**: Facilita backups e evita deletar o "cérebro" do bot num deploy acidental.

### Robustez e Correção de Estado
- [ ] **SyncOrdersOnStartup (O "Fim do Ponto Cego")**
  - **O que é**: Ao iniciar, o bot deve consultar a API da Binance (GetOpenOrders) e comparar com o transactions.json. Se uma ordem está "Open" no JSON mas não existe na Binance, ele deve checar se foi FILLED ou CANCELED e atualizar o JSON antes de começar.
  - **Por que**: Resolve o problema de perder trades se o bot reiniciar enquanto o mercado se move.

- [ ] **Log de "Missed Opportunities" (Oportunidades Perdidas)**
  - **O que é**: Quando falhar por saldo insuficiente, registrar isso estruturadamente no CSV ou num log específico (ex: missed_orders.log).
  - **Por que**: Para você saber, no fim do mês, quanto dinheiro deixou de ganhar por falta de banca e ajustar o aporte.

- [ ] **Notificações Assíncronas (Worker de Telegram)**
  - **O que é**: Mover a lógica de notificações via Telegram para uma goroutine separada (worker) que verifica as últimas ordens a cada minuto.
  - **Por que**: Remove o IO bloqueante do Telegram da thread principal de trading, garantindo execução mais rápida e estável.

- [ ] **Reload de Config a Quente (Hot Reload)**
  - **O que é**: Permitir alterar o range_min ou range_max no arquivo config.yaml e o bot aplicar sem precisar reiniciar o processo (e perder fila no book).

### Evolução de Estratégia
- [ ] **Adaptador Mercado Bitcoin (Taxas 0.015%)**
  - **O que é**: Criar uma nova implementação da interface Exchange para conectar no MB.
  - **Por que**: Aproveitar as taxas 5x menores para grids ultra-rápidos (High Frequency), se houver liquidez.
