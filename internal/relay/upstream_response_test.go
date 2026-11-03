package relay

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestUpstreamDiagnosticsNeverReachCustomers(t *testing.T) {
	if _, _, _, _, err := safeCompletionFrame([]byte(`{"error":{"message":"internal credential diagnostic"}}`)); err == nil {
		t.Fatal("upstream error accepted")
	}
	cleaned, _, _, _, err := safeCompletionFrame([]byte(`{"choices":[],"private_debug":"internal credential diagnostic"}`))
	if err != nil || strings.Contains(string(cleaned), "private_debug") {
		t.Fatal("private metadata leaked")
	}
	for _, payload := range []string{"data: {\"error\":{\"message\":\"internal credential diagnostic\"}}\n\n", "data: <internal credential diagnostic>\n\n"} {
		response := httptest.NewRecorder()
		_, _, _, err := proxySSE(response, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload))})
		if err == nil || strings.Contains(response.Body.String(), "internal credential diagnostic") || !strings.Contains(response.Body.String(), "请稍后重试") {
			t.Fatalf("unsafe stream: %s", response.Body.String())
		}
	}
	payload := ": private credential diagnostic\n\ndata: {\"choices\":\ndata: [],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5}}\n\ndata: [DONE]\n\n"
	response := httptest.NewRecorder()
	in, cached, out, err := proxySSE(response, &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(payload))})
	if err != nil || in != 10 || cached != 0 || out != 5 || strings.Contains(response.Body.String(), "private credential") {
		t.Fatalf("multiline stream failed: %v", err)
	}
}
