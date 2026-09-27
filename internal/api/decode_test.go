package api

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestDecodeRequestBody(t *testing.T) {
	for _, tc := range []struct {
		name        string
		body        io.Reader
		wantMessage string
		wantName    string
	}{
		{name: "no body", wantMessage: "request body is required and must be a JSON object"},
		{name: "empty body", body: strings.NewReader(""), wantMessage: "request body is required and must be a JSON object"},
		{name: "whitespace body", body: strings.NewReader(" \t\r\n"), wantMessage: "request body is required and must be a JSON object"},
		{name: "empty object", body: strings.NewReader("{}")},
		{name: "valid object", body: strings.NewReader(`{"confirm_name":"APPLICATION"}`), wantName: "APPLICATION"},
		{name: "trailing whitespace", body: strings.NewReader("{\"confirm_name\":\"APPLICATION\"}\n\t "), wantName: "APPLICATION"},
		{name: "unknown field", body: strings.NewReader(`{"nope":true}`), wantMessage: `invalid JSON or unknown field: json: unknown field "nope"`},
		{name: "truncated JSON", body: strings.NewReader(`{"confirm_name":`), wantMessage: "invalid JSON or unknown field: unexpected EOF"},
		{name: "malformed JSON", body: strings.NewReader(`{"confirm_name":!}`), wantMessage: "invalid JSON or unknown field: invalid character '!' looking for beginning of value"},
		{name: "multiple objects", body: strings.NewReader(`{} {}`), wantMessage: "request must contain one JSON object"},
		{name: "oversized body", body: strings.NewReader(`{"confirm_name":"` + strings.Repeat("a", 512<<10) + `"}`), wantMessage: "invalid JSON or unknown field: http: request body too large"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodDelete, "/api/v1/storage/retained/example", tc.body)
			w := httptest.NewRecorder()
			var in struct {
				ConfirmName string `json:"confirm_name"`
			}
			ok := decode(w, r, &in)
			if tc.wantMessage == "" {
				if !ok || in.ConfirmName != tc.wantName || w.Body.Len() != 0 {
					t.Fatalf("decode = %v, name = %q, response = %s", ok, in.ConfirmName, w.Body.String())
				}
				return
			}
			if ok || w.Code != http.StatusBadRequest {
				t.Fatalf("decode = %v, status = %d; want false, 400", ok, w.Code)
			}
			if got := w.Header().Get("Content-Type"); got != "application/json" {
				t.Fatalf("Content-Type = %q; want application/json", got)
			}
			var response struct {
				Error struct {
					Code    string `json:"code"`
					Message string `json:"message"`
				} `json:"error"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &response); err != nil {
				t.Fatal(err)
			}
			if response.Error.Code != "invalid_request" || response.Error.Message != tc.wantMessage {
				t.Fatalf("error = %+v; want invalid_request: %s", response.Error, tc.wantMessage)
			}
		})
	}
}
