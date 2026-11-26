package mcp

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/chauveaul/vibe-rpc/internal/format"
	"github.com/chauveaul/vibe-rpc/internal/paths"
	"github.com/chauveaul/vibe-rpc/internal/pricing"
	"github.com/chauveaul/vibe-rpc/internal/scanner"
	"github.com/chauveaul/vibe-rpc/internal/stats"
)

const (
	ServerName      = "vibe-rpc"
	ProtocolVersion = "2024-11-05"
)

type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type rpcResponse struct {
	JSONRPC string    `json:"jsonrpc"`
	ID      any       `json:"id,omitempty"`
	Result  any       `json:"result,omitempty"`
	Error   *rpcError `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

var toolDefs = []map[string]any{
	{
		"name":        "get_lifetime_stats",
		"description": "Return combined lifetime coding stats across Antigravity and Claude Code (hours, sessions, tokens, cost, streak, breakdown by source).",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "get_today",
		"description": "Return today's combined coding activity (hours, prompts, tool calls, tokens, cost, lines changed).",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "get_top_files",
		"description": "Return the most frequently edited files across sessions.",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"limit": map[string]any{"type": "integer", "description": "Maximum files to return (default 10)"},
			},
		},
	},
	{
		"name":        "get_model_split",
		"description": "Return the breakdown of turns, tokens, and estimated API-equivalent cost by model family.",
		"inputSchema": map[string]any{"type": "object", "properties": map[string]any{}},
	},
	{
		"name":        "get_recap",
		"description": "Generate a standup-ready coding recap for a range (today, yesterday, week, or N days).",
		"inputSchema": map[string]any{
			"type": "object",
			"properties": map[string]any{
				"range": map[string]any{"type": "string", "description": "today | yesterday | week | 7d | 30d"},
			},
		},
	},
}

func loadAgg() *scanner.Aggregate {
	if agg := scanner.ReadAggregate(); agg != nil {
		return agg
	}
	res := scanner.Scan(scanner.ScanOptions{})
	if res.Aggregate != nil {
		return res.Aggregate
	}
	return &scanner.Aggregate{}
}

func HandleMessage(raw []byte, aggProvider func() *scanner.Aggregate) *rpcResponse {
	var req rpcRequest
	if err := json.Unmarshal(raw, &req); err != nil {
		return &rpcResponse{JSONRPC: "2.0", Error: &rpcError{Code: -32700, Message: "parse error"}}
	}
	if req.Method == "notifications/initialized" || req.Method == "notifications/cancelled" {
		return nil // notifications receive no response
	}
	if aggProvider == nil {
		aggProvider = loadAgg
	}

	switch req.Method {
	case "initialize":
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"protocolVersion": ProtocolVersion,
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": ServerName, "version": paths.Version},
			},
		}
	case "ping":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{}}
	case "tools/list":
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Result: map[string]any{"tools": toolDefs}}
	case "tools/call":
		var p struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: "invalid params"}}
		}
		payload, err := callTool(p.Name, p.Arguments, aggProvider())
		if err != nil {
			return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32602, Message: err.Error()}}
		}
		txt, _ := json.MarshalIndent(payload, "", "  ")
		return &rpcResponse{
			JSONRPC: "2.0",
			ID:      req.ID,
			Result: map[string]any{
				"content": []map[string]any{{"type": "text", "text": string(txt)}},
			},
		}
	default:
		return &rpcResponse{JSONRPC: "2.0", ID: req.ID, Error: &rpcError{Code: -32601, Message: "method not found: " + req.Method}}
	}
}

func callTool(name string, args map[string]any, agg *scanner.Aggregate) (any, error) {
	if agg == nil {
		agg = &scanner.Aggregate{}
	}
	switch name {
	case "get_lifetime_stats":
		totalTok := agg.InputTokens + agg.OutputTokens + agg.CacheReadTokens + agg.CacheWriteTokens
		return map[string]any{
			"sessions":      agg.Sessions,
			"activeHours":   format.FmtHours(agg.ActiveMs),
			"userMessages":  agg.UserMessages,
			"toolCalls":     agg.ToolCalls,
			"tokens":        totalTok,
			"tokensFmt":     format.FmtNum(totalTok),
			"estimatedCost": pricing.FmtCost(agg.EstimatedCost),
			"streak":        agg.Streak,
			"longestStreak": agg.LongestStreak,
			"linesAdded":    agg.LinesAdded,
			"linesRemoved":  agg.LinesRemoved,
			"ships":         agg.Ships,
			"bySource":      agg.BySource,
		}, nil

	case "get_today":
		dk := scanner.DayKey(time.Now().UnixMilli())
		d := &scanner.DayBucket{}
		if agg.ByDay != nil && agg.ByDay[dk] != nil {
			d = agg.ByDay[dk]
		}
		tok := d.InputTokens + d.OutputTokens + d.CacheReadTokens + d.CacheWriteTokens
		return map[string]any{
			"day":          dk,
			"activeHours":  format.FmtHours(d.ActiveMs),
			"sessions":     d.Sessions,
			"prompts":      d.UserMessages,
			"toolCalls":    d.ToolCalls,
			"tokens":       tok,
			"tokensFmt":    format.FmtNum(tok),
			"cost":         pricing.FmtCost(d.Cost),
			"linesAdded":   d.LinesAdded,
			"linesRemoved": d.LinesRemoved,
			"ships":        d.Ships,
		}, nil

	case "get_top_files":
		limit := 10
		if v, ok := args["limit"].(float64); ok && v > 0 {
			limit = int(v)
		}
		list := agg.TopEditedFiles
		if len(list) > limit {
			list = list[:limit]
		}
		return list, nil

	case "get_model_split":
		return agg.ModelSplit, nil

	case "get_recap":
		rng := "yesterday"
		if v, ok := args["range"].(string); ok && v != "" {
			rng = v
		}
		rec := stats.BuildRecap(agg, rng)
		return map[string]any{
			"recap":    rec,
			"markdown": stats.RenderRecapMarkdown(rec),
		}, nil

	default:
		return nil, fmt.Errorf("unknown tool: %s", name)
	}
}

func ServeStdio(in io.Reader, out io.Writer) error {
	r := bufio.NewReader(in)
	for {
		line, err := r.ReadBytes('\n')
		if len(line) > 0 {
			if resp := HandleMessage(line, nil); resp != nil {
				b, _ := json.Marshal(resp)
				_, _ = out.Write(append(b, '\n'))
			}
		}
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}
	}
}

func InstallConfig(configPath, binPath string) error {
	if configPath == "" {
		configPath = paths.AgyMcpConfigPath()
	}
	if binPath == "" {
		exe, err := os.Executable()
		if err == nil {
			if real, err := filepath.EvalSymlinks(exe); err == nil {
				binPath = real
			} else {
				binPath = exe
			}
		} else {
			binPath = "vibe-rpc"
		}
	}
	root := map[string]any{}
	if b, err := os.ReadFile(configPath); err == nil && len(b) > 0 {
		if err := json.Unmarshal(b, &root); err != nil {
			return fmt.Errorf("parse %s: %w", configPath, err)
		}
	} else if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok || servers == nil {
		servers = map[string]any{}
	}
	servers[ServerName] = map[string]any{
		"command": binPath,
		"args":    []string{"mcp"},
	}
	root["mcpServers"] = servers
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return err
	}
	return paths.WriteFileAtomic(configPath, append(out, '\n'), 0o644)
}

func UninstallConfig(configPath string) (bool, error) {
	if configPath == "" {
		configPath = paths.AgyMcpConfigPath()
	}
	b, err := os.ReadFile(configPath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, err
	}
	root := map[string]any{}
	if err := json.Unmarshal(b, &root); err != nil {
		return false, err
	}
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok || servers == nil {
		return false, nil
	}
	if _, exists := servers[ServerName]; !exists {
		return false, nil
	}
	delete(servers, ServerName)
	root["mcpServers"] = servers
	out, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, err
	}
	return true, paths.WriteFileAtomic(configPath, append(out, '\n'), 0o644)
}
