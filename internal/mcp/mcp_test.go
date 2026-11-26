package mcp

import (
	"encoding/json"
	"testing"

	"github.com/meze/agy-rpc/internal/scanner"
)

func TestMCPProtocolHandlers(t *testing.T) {
	agg := &scanner.Aggregate{
		Sessions:      5,
		ActiveMs:      7_200_000,
		UserMessages:  42,
		EstimatedCost: 3.50,
		BySource: map[string]*scanner.SourceStats{
			"antigravity": {Sessions: 3, ActiveMs: 4_000_000},
			"claude":      {Sessions: 2, ActiveMs: 3_200_000},
		},
	}
	prov := func() *scanner.Aggregate { return agg }

	resp := HandleMessage([]byte(`{"jsonrpc":"2.0","id":1,"method":"initialize"}`), prov)
	if resp == nil || resp.Error != nil {
		t.Fatalf("initialize failed: %+v", resp)
	}

	resp = HandleMessage([]byte(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`), prov)
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/list failed: %+v", resp)
	}

	resp = HandleMessage([]byte(`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"get_lifetime_stats","arguments":{}}}`), prov)
	if resp == nil || resp.Error != nil {
		t.Fatalf("tools/call failed: %+v", resp)
	}
	b, _ := json.Marshal(resp.Result)
	if len(b) < 20 {
		t.Fatalf("unexpected tool result: %s", string(b))
	}
}
