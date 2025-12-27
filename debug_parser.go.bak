package main

import (
	"encoding/json"
	"fmt"
)

func main() {
	jsonStr := `{"e":"executionReport","E":1765836148316,"s":"BTCUSDT","c":"BUY_1765836053234_L2","S":"BUY","o":"LIMIT","f":"GTC","q":"0.00006000","p":"86022.48000000","P":"0.00000000","F":"0.00000000","g":-1,"C":"","x":"NEW","X":"NEW","r":"NONE","i":53980853294,"l":"0.00000000","z":"0.00000000","L":"0.00000000","n":"0","N":null,"T":1765836148315,"t":-1,"I":114458196389,"w":true,"m":false,"M":false,"O":1765836148315,"Z":"0.00000000","Y":"0.00000000","Q":"0.00000000","W":1765836148315,"V":"EXPIRE_MAKER"}`

	var baseEvent struct {
		Event string `json:"e"`
	}

	err := json.Unmarshal([]byte(jsonStr), &baseEvent)
	if err != nil {
		fmt.Printf("❌ Unmarshal Error: %v\n", err)
	} else {
		fmt.Printf("✅ Success! Event: %s\n", baseEvent.Event)
	}
}
