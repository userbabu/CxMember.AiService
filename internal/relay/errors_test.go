package relay

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestCustomerErrorsPreserveUnifiedBusinessMessage(t *testing.T) {
	var logs bytes.Buffer
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&logs, nil)))
	defer slog.SetDefault(previous)
	message := "您的账户异常：暂时无法对话"
	for _, status := range []int{400, 401, 402, 403, 404, 429, 502, 503} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			response := httptest.NewRecorder()
			id := newRequestID()
			response.Header().Set("X-Request-ID", id)
			openAIError(response, status, "business_error", message)
			var payload struct {
				Code      int    `json:"code"`
				Message   string `json:"msg"`
				RequestID string `json:"request_id"`
				Data      any    `json:"data"`
				Errors    any    `json:"errors"`
				Error     struct {
					Message   string `json:"message"`
					Type      string `json:"type"`
					RequestID string `json:"request_id"`
				} `json:"error"`
			}
			if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
				t.Fatal(err)
			}
			if response.Code != status || payload.Code != status || payload.Message != message || payload.Error.Message != message || payload.Error.Type != "business_error" || payload.RequestID != id || payload.Error.RequestID != id || payload.Data != nil || payload.Errors != nil {
				t.Fatalf("unified error contract mismatch: %s", response.Body.String())
			}
			if response.Header().Get("X-Request-ID") != id || !strings.Contains(response.Header().Get("Content-Type"), "application/json") {
				t.Fatal("error headers missing")
			}
			if !strings.Contains(logs.String(), message) || !strings.Contains(logs.String(), id) {
				t.Fatal("business failure trace missing from server log")
			}
		})
	}
}
