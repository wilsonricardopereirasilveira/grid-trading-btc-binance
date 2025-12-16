# 🗺️ Roadmap de Evolução - Grid Trading Bot

## 🔴 Prioridade P0: Crítico (Segurança e Estabilidade)
**Foco**: Corrigir vulnerabilidades que podem causar perda de controle ou cegueira do bot.

- [ ] **Fila de Retry no Telegram (Resiliência)**
  - **O que é**: Implementar uma lógica simples de "tente de novo" se o envio da mensagem falhar (erro de net/timeout).
  - **Por que**: Atualmente, se a internet piscar no segundo do alerta de "Saldo Baixo", você nunca fica sabendo.

- [ ] **Flag PAUSE_BUYS (Botão de Pânico Suave)**
  - **O que é**: Ler uma variável de ambiente (ou arquivo .env) a cada ciclo. Se PAUSE_BUYS=true, o bot pula a função de criar novas ordens, mas mantém a verificação de vendas (Take Profit).
  - **Por que**: Permite que você pare de aumentar a exposição ao risco sem desligar o bot (que mataria a saída de lucro).

- [ ] **Organização de Arquivos (Seu item)**
  - **O que é**: Definir um diretório fixo (ex: /data/) para transactions.json e logs, separando código de dados.
  - **Por que**: Facilita backups e evita deletar o "cérebro" do bot num deploy acidental.

## 🟠 Prioridade P1: Alto (Robustez e Correção de Estado)
**Foco**: Garantir que o bot saiba se recuperar sozinho de reinícios e falhas.

- [ ] **SyncOrdersOnStartup (O "Fim do Ponto Cego")**
  - **O que é**: Ao iniciar, o bot deve consultar a API da Binance (GetOpenOrders) e comparar com o transactions.json. Se uma ordem está "Open" no JSON mas não existe na Binance, ele deve checar se foi FILLED ou CANCELED e atualizar o JSON antes de começar.
  - **Por que**: Resolve o problema de perder trades se o bot reiniciar enquanto o mercado se move.

- [ ] **Log de "Missed Opportunities" (Oportunidades Perdidas)**
  - **O que é**: Quando falhar por saldo insuficiente, registrar isso estruturadamente no CSV ou num log específico (ex: missed_orders.log).
  - **Por que**: Para você saber, no fim do mês, quanto dinheiro deixou de ganhar por falta de banca e ajustar o aporte.

## 🟡 Prioridade P2: Médio (Observabilidade e UX)
**Foco**: Melhorar a visão do que está acontecendo sem ler logs brutos.

- [ ] **Dashboard Grafana (Visualização)**
  - **O que é**: Subir um container Grafana + InfluxDB (ou ler direto do CSV) para plotar os gráficos de: unrealized_pnl, burn_rate (taxas) e utilização do grid.
  - **Por que**: Transforma dados brutos em inteligência visual para tomada de decisão na sexta-feira.

- [ ] **Reload de Config a Quente (Hot Reload)**
  - **O que é**: Permitir alterar o range_min ou range_max no arquivo config.yaml e o bot aplicar sem precisar reiniciar o processo (e perder fila no book).

## 🔵 Prioridade P3: Futuro (Evolução de Estratégia)
**Foco**: Mudar a lógica para ganhar mais ou gastar menos.

- [ ] **Estratégia Maker-Maker (Binance)**
  - **O que é**: Mudar a venda de Market (Taker) para Limit (Maker) para economizar taxas e pegar "agulhadas".
  - **Requisito**: Exige refatoração pesada da gestão de estado (linkar ordem de venda com a de compra no JSON).

- [ ] **Adaptador Mercado Bitcoin (Taxas 0.015%)**
  - **O que é**: Criar uma nova implementação da interface Exchange para conectar no MB.
  - **Por que**: Aproveitar as taxas 5x menores para grids ultra-rápidos (High Frequency), se houver liquidez.
