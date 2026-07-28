package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestTerminalFramesKeepOutputAndExitStatus(t *testing.T) {
	var out bytes.Buffer
	err := readTerminalOutput(strings.NewReader(": heartbeat\n\ndata: {\"type\":\"output\",\"data\":\"aGVsbG8NCg==\"}\n\ndata: {\"type\":\"exit\",\"code\":0,\"message\":\"Process exited\"}\n\n"), &out)
	if err != nil || out.String() != "hello\r\n" {
		t.Fatal(out.String(), err)
	}
	for _, input := range []string{`data: {"type":"exit","code":2,"message":"Process exited"}`, `data: {"type":"output","data":"!"}`, `data: {"type":"unknown"}`, ""} {
		if err := readTerminalOutput(strings.NewReader(input), &out); err == nil {
			t.Fatal("invalid/failed stream accepted", input)
		}
	}
}
