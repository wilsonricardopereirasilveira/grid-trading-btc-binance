package main

import (
	"encoding/json"
	"fmt"
)

type OrderUpdate struct {
	Event         string `json:"e"`
	EventTime     int64  `json:"E"`
	Symbol        string `json:"s"`
	ClientOrderID string `json:"c"`
	Side          string `json:"S"`
	Type          string `json:"o"`
	TimeInForce   string `json:"f"`
	Quantity      string `json:"q"`
	Price         string `json:"p"`
	StopPrice     string `json:"P"`
	IcebergQty    string `json:"F"`
	OrderListId   int64  `json:"g"`
	OriginalID    string `json:"C"`
	ExecutionType string `json:"x"`
	Status        string `json:"X"`
	RejectReason  string `json:"r"`
	OrderID       int64  `json:"i"`
	LastExecQty   string `json:"l"`
	CumExecQty    string `json:"z"`
	LastExecPrice string `json:"L"`
	Commission    string `json:"n"`
	CommAsset     string `json:"N"`
	TxTime        int64  `json:"T"`
	TradeID       int64  `json:"t"`
	Ignore        int64  `json:"I"`
	IsWorking     bool   `json:"w"`
	IsMaker       bool   `json:"m"`
}

func main() {
	jsonStr := `{"e":"executionReport","E":1765837416297,"s":"BTCUSDT","c":"BUY_1765837416153_L3","S":"BUY","o":"LIMIT","f":"GTC","q":"0.00006000","p":"85893.44000000","P":"0.00000000","F":"0.00000000","g":-1,"C":"","x":"TRADE","X":"FILLED","r":"NONE","i":53981412716,"l":"0.00006000","z":"0.00006000","L":"85888.89000000","n":"0.00000453","N":"BNB","T":1765837416297,"t":5662542871,"I":114459376815,"w":false,"m":false,"M":true,"O":1765837416297,"Z":"5.15333340","Y":"5.15333340","Q":"0.00000000","W":1765837416297,"V":"EXPIRE_MAKER"}`

	var event OrderUpdate
	err := json.Unmarshal([]byte(jsonStr), &event)
	if err != nil {
		fmt.Printf("❌ Unmarshal Error: %v\n", err)
	} else {
		fmt.Printf("✅ Success! Type: %s\n", event.Type)
	}
}
