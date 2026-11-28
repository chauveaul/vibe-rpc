package main

import (
	"io"
	"os"
	"testing"

	"github.com/chauveaul/vibe-rpc/internal/config"
)

func TestChooseAgentsFlag(t *testing.T) {
	got, err := chooseAgents("agy, opencode", os.Stdin, io.Discard)
	if err != nil || !got["antigravity"] || !got["opencode"] || got["claude"] {
		t.Fatalf("got %v, %v", got, err)
	}
	if _, err := chooseAgents("cursor", os.Stdin, io.Discard); err == nil {
		t.Fatal("unknown agents should error")
	}
	all, _ := chooseAgents("all", os.Stdin, io.Discard)
	if len(all) != 4 {
		t.Fatalf("all = %v", all)
	}
}

func TestSaveTrackedAgentsPersistsCodex(t *testing.T) {
	t.Setenv("VIBE_RPC_CONFIG", t.TempDir()+"/config.json")
	if err := saveTrackedAgents(map[string]bool{"claude": true}); err != nil {
		t.Fatal(err)
	}
	s := config.Load().Sources
	if s.Tracks("codex") || s.Tracks("opencode") || !s.Tracks("claude") {
		t.Fatalf("unticked agents should stay untracked, got %+v", s)
	}
	if err := saveTrackedAgents(map[string]bool{"codex": true}); err != nil {
		t.Fatal(err)
	}
	if s := config.Load().Sources; !s.Tracks("codex") || s.Tracks("claude") {
		t.Fatalf("re-picking codex should track it, got %+v", s)
	}
}
