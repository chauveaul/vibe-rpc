package discord

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
)

const (
	OpHandshake uint32 = 0
	OpFrame     uint32 = 1
	OpClose     uint32 = 2
	OpPing      uint32 = 3
	OpPong      uint32 = 4

	ARRPCUserID = "1045800378228281345"
)

var (
	ErrAbsent  = errors.New("could not connect to Discord client")
	ErrTimeout = errors.New("Discord RPC request timed out")
	ErrClosed  = errors.New("Discord IPC connection closed")
)

type User struct {
	ID       string `json:"id"`
	Username string `json:"username"`
}

func IsBridgeUser(u *User) bool {
	if u == nil {
		return false
	}
	return u.ID == ARRPCUserID || strings.EqualFold(u.Username, "arrpc")
}

type Button struct {
	Label string `json:"label"`
	URL   string `json:"url"`
}

type Activity struct {
	Name           string   `json:"name,omitempty"`
	Type           int      `json:"type"`
	Details        string   `json:"details,omitempty"`
	State          string   `json:"state,omitempty"`
	StartTimestamp int64    `json:"startTimestamp,omitempty"`
	EndTimestamp   int64    `json:"endTimestamp,omitempty"`
	LargeImageKey  string   `json:"largeImageKey,omitempty"`
	LargeImageText string   `json:"largeImageText,omitempty"`
	SmallImageKey  string   `json:"smallImageKey,omitempty"`
	SmallImageText string   `json:"smallImageText,omitempty"`
	PartyID        string   `json:"partyId,omitempty"`
	PartySize      int      `json:"partySize,omitempty"`
	PartyMax       int      `json:"partyMax,omitempty"`
	Buttons        []Button `json:"buttons,omitempty"`
}

func FormatActivity(a Activity, pid int) map[string]any {
	if pid == 0 {
		pid = os.Getpid()
	}
	act := map[string]any{
		"name":     a.Name,
		"type":     a.Type,
		"instance": false,
	}
	if a.Details != "" {
		act["details"] = a.Details
	}
	if a.State != "" {
		act["state"] = a.State
	}
	if a.StartTimestamp > 0 || a.EndTimestamp > 0 {
		ts := map[string]int64{}
		if a.StartTimestamp > 0 {
			ts["start"] = a.StartTimestamp
		}
		if a.EndTimestamp > 0 {
			ts["end"] = a.EndTimestamp
		}
		act["timestamps"] = ts
	}
	if a.LargeImageKey != "" || a.SmallImageKey != "" || a.LargeImageText != "" || a.SmallImageText != "" {
		assets := map[string]string{}
		if a.LargeImageKey != "" {
			assets["large_image"] = a.LargeImageKey
		}
		if a.SmallImageKey != "" {
			assets["small_image"] = a.SmallImageKey
		}
		if a.LargeImageText != "" {
			assets["large_text"] = a.LargeImageText
		}
		if a.SmallImageText != "" {
			assets["small_text"] = a.SmallImageText
		}
		act["assets"] = assets
	}
	if a.PartyID != "" || (a.PartySize > 0 && a.PartyMax > 0) {
		party := map[string]any{}
		if a.PartyID != "" {
			party["id"] = a.PartyID
		}
		if a.PartySize > 0 && a.PartyMax > 0 {
			party["size"] = []int{a.PartySize, a.PartyMax}
		}
		act["party"] = party
	}
	if len(a.Buttons) > 0 {
		act["buttons"] = a.Buttons
	}
	return map[string]any{
		"pid":      pid,
		"activity": act,
	}
}

func EncodeFrame(op uint32, payload any) ([]byte, error) {
	var body []byte
	var err error
	if payload != nil {
		body, err = json.Marshal(payload)
		if err != nil {
			return nil, err
		}
	}
	buf := bytes.NewBuffer(make([]byte, 0, 8+len(body)))
	_ = binary.Write(buf, binary.LittleEndian, op)
	_ = binary.Write(buf, binary.LittleEndian, uint32(len(body)))
	buf.Write(body)
	return buf.Bytes(), nil
}

func ReadFrame(r io.Reader) (uint32, []byte, error) {
	var hdr [8]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return 0, nil, err
	}
	op := binary.LittleEndian.Uint32(hdr[0:4])
	length := binary.LittleEndian.Uint32(hdr[4:8])
	if length > 4*1024*1024 {
		return 0, nil, fmt.Errorf("frame too large: %d", length)
	}
	body := make([]byte, length)
	if length > 0 {
		if _, err := io.ReadFull(r, body); err != nil {
			return 0, nil, err
		}
	}
	return op, body, nil
}

type Client struct {
	ClientID string
	DialFn   func() (net.Conn, error) // injectable for tests

	mu           sync.Mutex
	conn         net.Conn
	user         *User
	pending      map[string]chan rpcReply
	readyCh      chan error
	onDisconnect func()
	closed       bool
}

type rpcReply struct {
	err error
}

func NewClient(clientID string, onDisconnect func()) *Client {
	return &Client{
		ClientID:     clientID,
		pending:      map[string]chan rpcReply{},
		onDisconnect: onDisconnect,
	}
}

func (c *Client) User() *User {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.user
}

func (c *Client) Connected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.conn != nil && c.user != nil && !c.closed
}

func (c *Client) openConn() (net.Conn, error) {
	if c.DialFn != nil {
		return c.DialFn()
	}
	cands := CandidatePaths()
	for _, p := range cands {
		conn, err := dialPath(p, 1500*time.Millisecond)
		if err == nil {
			return conn, nil
		}
	}
	return nil, ErrAbsent
}

func (c *Client) Login() error {
	conn, err := c.openConn()
	if err != nil {
		return err
	}
	c.mu.Lock()
	c.conn = conn
	c.closed = false
	c.readyCh = make(chan error, 1)
	readyCh := c.readyCh
	c.mu.Unlock()

	go c.readLoop(conn)

	if err := c.writeFrame(OpHandshake, map[string]any{"v": 1, "client_id": c.ClientID}); err != nil {
		c.Close()
		return err
	}

	select {
	case err := <-readyCh:
		if err != nil {
			c.Close()
			return err
		}
		return nil
	case <-time.After(10 * time.Second):
		c.Close()
		return ErrTimeout
	}
}

func (c *Client) writeFrame(op uint32, payload any) error {
	frame, err := EncodeFrame(op, payload)
	if err != nil {
		return err
	}
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return ErrClosed
	}
	_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	_, err = conn.Write(frame)
	return err
}

func (c *Client) readLoop(conn net.Conn) {
	defer func() {
		c.handleDrop(conn)
	}()
	for {
		op, body, err := ReadFrame(conn)
		if err != nil {
			return
		}
		switch op {
		case OpPing:
			var raw any
			_ = json.Unmarshal(body, &raw)
			_ = c.writeFrame(OpPong, raw)
		case OpClose:
			return
		case OpFrame:
			var msg struct {
				Cmd   string          `json:"cmd"`
				Evt   string          `json:"evt"`
				Nonce string          `json:"nonce"`
				Data  json.RawMessage `json:"data"`
			}
			if err := json.Unmarshal(body, &msg); err != nil {
				continue
			}
			if msg.Cmd == "DISPATCH" && msg.Evt == "READY" {
				var readyData struct {
					User User `json:"user"`
				}
				_ = json.Unmarshal(msg.Data, &readyData)
				c.mu.Lock()
				u := readyData.User
				c.user = &u
				ch := c.readyCh
				c.readyCh = nil
				c.mu.Unlock()
				if ch != nil {
					ch <- nil
				}
				continue
			}
			if msg.Nonce != "" {
				c.mu.Lock()
				ch, ok := c.pending[msg.Nonce]
				if ok {
					delete(c.pending, msg.Nonce)
				}
				c.mu.Unlock()
				if ok {
					if msg.Evt == "ERROR" {
						var eData struct {
							Message string `json:"message"`
						}
						_ = json.Unmarshal(msg.Data, &eData)
						if eData.Message == "" {
							eData.Message = "Discord RPC error"
						}
						ch <- rpcReply{err: errors.New(eData.Message)}
					} else {
						ch <- rpcReply{}
					}
				}
			}
		}
	}
}

func (c *Client) handleDrop(conn net.Conn) {
	c.mu.Lock()
	if c.conn != conn {
		c.mu.Unlock()
		return
	}
	wasConnected := c.user != nil && !c.closed
	_ = conn.Close()
	c.conn = nil
	c.user = nil
	if c.readyCh != nil {
		c.readyCh <- ErrClosed
		c.readyCh = nil
	}
	for k, ch := range c.pending {
		ch <- rpcReply{err: ErrClosed}
		delete(c.pending, k)
	}
	cb := c.onDisconnect
	c.mu.Unlock()
	if wasConnected && cb != nil {
		cb()
	}
}

func (c *Client) Request(cmd string, args any) error {
	nonce := uuid.NewString()
	ch := make(chan rpcReply, 1)
	c.mu.Lock()
	if c.conn == nil || c.closed {
		c.mu.Unlock()
		return ErrClosed
	}
	c.pending[nonce] = ch
	c.mu.Unlock()

	if err := c.writeFrame(OpFrame, map[string]any{"cmd": cmd, "args": args, "nonce": nonce}); err != nil {
		c.mu.Lock()
		delete(c.pending, nonce)
		c.mu.Unlock()
		return err
	}

	select {
	case rep := <-ch:
		return rep.err
	case <-time.After(10 * time.Second):
		c.mu.Lock()
		delete(c.pending, nonce)
		c.mu.Unlock()
		return ErrTimeout
	}
}

func (c *Client) SetActivity(a Activity) error {
	return c.Request("SET_ACTIVITY", FormatActivity(a, os.Getpid()))
}

func (c *Client) ClearActivity() error {
	return c.Request("SET_ACTIVITY", map[string]any{"pid": os.Getpid()})
}

func (c *Client) Close() {
	c.mu.Lock()
	c.closed = true
	conn := c.conn
	c.conn = nil
	c.user = nil
	for k, ch := range c.pending {
		ch <- rpcReply{err: ErrClosed}
		delete(c.pending, k)
	}
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}
