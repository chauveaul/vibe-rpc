package main

import (
	"io"
	"os"
	"testing"
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
	if len(all) != 3 {
		t.Fatalf("all = %v", all)
	}
}
