//go:build windows

package discord

import (
	"fmt"
	"net"
	"os"
	"time"
)

func CandidatePaths() []string {
	out := make([]string, 0, 10)
	for i := 0; i < 10; i++ {
		out = append(out, fmt.Sprintf(`\\.\pipe\discord-ipc-%d`, i))
	}
	return out
}

type pipeConn struct{ *os.File }

func (p pipeConn) LocalAddr() net.Addr                { return pipeAddr(p.Name()) }
func (p pipeConn) RemoteAddr() net.Addr               { return pipeAddr(p.Name()) }
func (p pipeConn) SetDeadline(t time.Time) error      { return p.File.SetDeadline(t) }
func (p pipeConn) SetReadDeadline(t time.Time) error  { return p.File.SetReadDeadline(t) }
func (p pipeConn) SetWriteDeadline(t time.Time) error { return p.File.SetWriteDeadline(t) }

type pipeAddr string

func (a pipeAddr) Network() string { return "pipe" }
func (a pipeAddr) String() string  { return string(a) }

func dialPath(p string, _ time.Duration) (net.Conn, error) {
	f, err := os.OpenFile(p, os.O_RDWR, 0)
	if err != nil {
		return nil, err
	}
	return pipeConn{f}, nil
}
