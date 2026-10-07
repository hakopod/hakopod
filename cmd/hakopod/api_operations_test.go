package main

import "testing"

func TestReorderAgentOptIns(t *testing.T) {
	got := reorder([]string{"call", "scaleService", "--allow-write", "--path-json", "{}", "--allow-exec", "--allow-sql", "--allow-sql-write"})
	want := []string{"--allow-write", "--path-json", "{}", "--allow-exec", "--allow-sql", "--allow-sql-write", "call", "scaleService"}
	if len(got) != len(want) {
		t.Fatal(got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatal(got)
		}
	}
}
