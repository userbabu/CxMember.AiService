package relay

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const diagnosticLimit = 64 << 10

type chatFailure struct {
	stage          string
	provider       string
	upstreamModel  string
	upstreamStatus int
	upstreamBody   string
	truncated      bool
	secret         string
	err            error
}

// 只缓存错误响应，成功内容和聊天消息不进入诊断记录；保持流式刷新能力。
type chatFailureWriter struct {
	http.ResponseWriter
	failure *chatFailure
	status  int
	body    bytes.Buffer
}

func (f *chatFailure) capture(data []byte) {
	f.truncated = len(data) > diagnosticLimit
	if f.truncated {
		data = data[:diagnosticLimit]
	}
	f.upstreamBody = string(data)
}
func (w *chatFailureWriter) WriteHeader(status int) {
	if w.status == 0 {
		w.status = status
		w.ResponseWriter.WriteHeader(status)
	}
}
func (w *chatFailureWriter) Write(p []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if w.status >= 400 && w.body.Len() < diagnosticLimit {
		n := diagnosticLimit - w.body.Len()
		if len(p) < n {
			n = len(p)
		}
		w.body.Write(p[:n])
	}
	return w.ResponseWriter.Write(p)
}
func (w *chatFailureWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	_ = http.NewResponseController(w.ResponseWriter).Flush()
}
func (w *chatFailureWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

var diagnosticSecrets = regexp.MustCompile(`(?i)("?(?:api[_-]?key|access[_-]?token|refresh[_-]?token|authorization|password|secret|cookie|token)"?\s*[:=]\s*)("[^"\r\n]*"|'[^'\r\n]*'|[^\s,;}]+)`)
var diagnosticBearer = regexp.MustCompile(`(?i)\bBearer\s+[A-Za-z0-9._~+/=-]+`)
var diagnosticKey = regexp.MustCompile(`\b(?:sk-[A-Za-z0-9_-]+|eyJ[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+\.[A-Za-z0-9_-]+)\b`)
var diagnosticURL = regexp.MustCompile(`(https?://)[^\s/@:]+:[^\s/@]+@`)

func redactDiagnostic(value, secret string) string {
	if secret != "" {
		value = strings.ReplaceAll(value, secret, "[redacted]")
	}
	value = diagnosticSecrets.ReplaceAllString(value, `${1}"[redacted]"`)
	value = diagnosticBearer.ReplaceAllString(value, "Bearer [redacted]")
	value = diagnosticKey.ReplaceAllString(value, "[redacted]")
	value = diagnosticURL.ReplaceAllString(value, "${1}[redacted]@")
	return strings.ToValidUTF8(value, "�")
}

func (s *Server) saveChatFailure(r *http.Request, w *chatFailureWriter, req *chatRequest, f *chatFailure, started time.Time) {
	if w.status < 400 && f.err == nil {
		return
	}
	kind, message := "request_failed", ""
	var response struct {
		Error struct {
			Type    string `json:"type"`
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(w.body.Bytes(), &response) == nil && response.Error.Type != "" {
		kind = response.Error.Type
		message = response.Error.Message
	}
	cause := ""
	if f.err != nil {
		cause = f.err.Error()
		var timeout net.Error
		switch {
		case errors.Is(f.err, context.Canceled):
			kind = "request_cancelled"
		case errors.Is(f.err, context.DeadlineExceeded):
			kind = "upstream_timeout"
		case errors.As(f.err, &timeout) && timeout.Timeout():
			kind = "upstream_timeout"
		}
	}
	detail := map[string]any{"stage": f.stage, "http_status": w.status, "provider": f.provider, "upstream_model": f.upstreamModel,
		"upstream_status": f.upstreamStatus, "message": redactDiagnostic(message, f.secret), "cause": redactDiagnostic(cause, f.secret),
		"upstream_body": redactDiagnostic(f.upstreamBody, f.secret), "body_truncated": f.truncated, "started_at": started.UTC().Format(time.RFC3339Nano)}
	raw, err := json.Marshal(detail)
	if err != nil {
		slog.Error("[AIRelay] error detail encoding failed", "request_id", w.Header().Get("X-Request-ID"), "error", err)
		return
	}
	model := req.Model
	// 非法模型编号可能超过数据库列宽，诊断记录仍需保存；不向其复制请求正文。
	if len(model) > 120 {
		model = ""
	}
	if !utf8.ValidString(model) {
		model = ""
	}
	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), 5*time.Second)
	defer cancel()
	_, err = s.store.db.ExecContext(ctx, `INSERT INTO ai_usage(request_id,uid,model_id,status,error_code,error_detail,elapsed_ms) VALUES(?,?,?,'failed',?,?,?)`, w.Header().Get("X-Request-ID"), claimsOf(r).Subject, model, kind, string(raw), time.Since(started).Milliseconds())
	if err != nil {
		slog.Error("[AIRelay] failed request persistence failed", "request_id", w.Header().Get("X-Request-ID"), "error", err)
	}
}
