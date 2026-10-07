package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"testing"
)

func TestTerminalPollRingBoundsAndExactCursor(t *testing.T) {
	b := &terminalPollBuffer{cursor: 9007199254740992}
	writer := &terminalPollWriter{header: http.Header{}, buffer: b}
	for i := 0; i < 40; i++ {
		raw, _ := json.Marshal(map[string]string{"type": "output", "data": string(make([]byte, 4000))})
		fmt.Fprintf(writer, "data: %s\n\n", raw)
	}
	if b.cursor != 9007199254741032 || b.bytes > terminalPollBytes || len(b.frames) == 0 {
		t.Fatal("unbounded terminal replay", b.cursor, b.bytes)
	}
	if b.frames[0].Cursor <= 9007199254740993 {
		t.Fatal("overflow did not advance first available cursor")
	}
	for _, frame := range b.frames {
		if !json.Valid(frame.Data) {
			t.Fatal("invalid replay JSON")
		}
	}
}

func TestTerminalPollCursorLoss(t *testing.T) {
	b := &terminalPollBuffer{}
	for i := 0; i < 300; i++ {
		b.append([]byte(`{"type":"output","data":""}`))
	}
	sample, err := b.sample(0)
	if err != nil || sample["truncated"] != true || len(sample["frames"].([]terminalFrame)) != 256 || sample["next_cursor"] != "300" {
		t.Fatal("cursor loss not disclosed", sample, err)
	}
	if _, err = b.sample(301); err == nil {
		t.Fatal("future cursor accepted")
	}
	sample, err = b.sample(300)
	if err != nil || len(sample["frames"].([]terminalFrame)) != 0 || sample["truncated"] != false {
		t.Fatal("resume repeated output", sample, err)
	}
}
