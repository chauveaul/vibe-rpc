package discord

import (
	"encoding/json"
	"net"
	"testing"
)

func TestIPCClientHandshakeAndSetActivity(t *testing.T) {
	clientConn, serverConn := net.Pipe()
	defer serverConn.Close()

	// Mock Discord IPC peer over in-memory net.Pipe()
	serverErr := make(chan error, 1)
	go func() {
		// 1. Expect handshake
		op, body, err := ReadFrame(serverConn)
		if err != nil {
			serverErr <- err
			return
		}
		if op != OpHandshake {
			serverErr <- ErrClosed
			return
		}
		var hs map[string]any
		_ = json.Unmarshal(body, &hs)

		// Send READY dispatch
		ready, _ := EncodeFrame(OpFrame, map[string]any{
			"cmd": "DISPATCH",
			"evt": "READY",
			"data": map[string]any{
				"user": map[string]any{"id": "42", "username": "meze"},
			},
		})
		_, _ = serverConn.Write(ready)

		// 2. Expect SET_ACTIVITY
		op, body, err = ReadFrame(serverConn)
		if err != nil || op != OpFrame {
			serverErr <- err
			return
		}
		var req struct {
			Cmd   string         `json:"cmd"`
			Nonce string         `json:"nonce"`
			Args  map[string]any `json:"args"`
		}
		_ = json.Unmarshal(body, &req)
		ack, _ := EncodeFrame(OpFrame, map[string]any{
			"cmd":   req.Cmd,
			"nonce": req.Nonce,
			"data":  map[string]any{},
		})
		_, _ = serverConn.Write(ack)
		serverErr <- nil
	}()

	c := NewClient("1575595772760559656", nil)
	c.DialFn = func() (net.Conn, error) { return clientConn, nil }
	defer c.Close()

	if err := c.Login(); err != nil {
		t.Fatalf("Login failed: %v", err)
	}
	if !c.Connected() || c.User() == nil || c.User().Username != "meze" {
		t.Fatalf("expected connected user meze, got %+v", c.User())
	}
	if IsBridgeUser(c.User()) {
		t.Fatalf("desktop user should not be classified as arRPC bridge")
	}
	if !IsBridgeUser(&User{ID: ARRPCUserID, Username: "arrpc"}) {
		t.Fatalf("arRPC user should be detected as bridge")
	}

	err := c.SetActivity(Activity{
		Name:          "Antigravity",
		Type:          0,
		Details:       "Working in vibe-rpc",
		State:         "Edit · main.go",
		LargeImageKey: "https://cdn.jsdelivr.net/gh/chauveaul/vibe-rpc@main/internal/server/assets/gifs/agy-working.gif",
	})
	if err != nil {
		t.Fatalf("SetActivity failed: %v", err)
	}
	if err := <-serverErr; err != nil {
		t.Fatalf("mock peer error: %v", err)
	}
}
