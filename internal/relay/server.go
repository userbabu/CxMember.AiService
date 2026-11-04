package relay

import (
	"bufio"
	"context"
	"crypto/rand"
	"crypto/subtle"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type Server struct {
	cfg    Config
	store  *Store
	client *http.Client
}

func NewServer(cfg Config, store *Store) *Server {
	// 并发对话共用上游连接池，保留足够空闲连接供下一轮复用，避免重复 TLS 握手。
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConnsPerHost = 32
	return &Server{cfg: cfg, store: store, client: &http.Client{Transport: transport, Timeout: cfg.UpstreamTimeout}}
}
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { writeSuccess(w, map[string]any{"status": "ok"}) })
	mux.Handle("POST /internal/v1/tokens", s.admin(http.HandlerFunc(s.registerToken)))
	mux.Handle("GET /v1/models", s.member(http.HandlerFunc(s.models)))
	mux.Handle("GET /v1/service/config", s.member(http.HandlerFunc(s.ederServiceConfig)))
	mux.Handle("POST /v1/client/initialize", s.member(http.HandlerFunc(s.initializeClient)))
	mux.Handle("GET /v1/account/quota", s.member(http.HandlerFunc(s.quota)))
	mux.Handle("GET /v1/account/usage", s.member(http.HandlerFunc(s.usage)))
	mux.Handle("POST /v1/generations", s.member(http.HandlerFunc(s.createGeneration)))
	mux.Handle("GET /v1/generations", s.member(http.HandlerFunc(s.listGenerations)))
	mux.Handle("GET /v1/generations/{id}", s.member(http.HandlerFunc(s.getGeneration)))
	mux.Handle("GET /v1/generations/{id}/content", s.member(http.HandlerFunc(s.generationContent)))
	mux.Handle("POST /v1/chat/completions", s.member(http.HandlerFunc(s.chat)))
	routes := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handler, pattern := mux.Handler(r)
		if pattern == "" {
			slog.Warn("[AIRelay] unmatched request", "method", r.Method, "path", r.URL.Path, "request_id", w.Header().Get("X-Request-ID"))
			if knownRoute(r.URL.Path) {
				openAIError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
				return
			}
			openAIError(w, http.StatusNotFound, "not_found", "route not found")
			return
		}
		handler.ServeHTTP(w, r)
	})
	return securityHeaders(requestTrace(agentPageCORS(routes)))
}

func knownRoute(path string) bool {
	switch path {
	case "/healthz", "/internal/v1/tokens", "/v1/models", "/v1/service/config", "/v1/client/initialize", "/v1/account/quota", "/v1/account/usage", "/v1/chat/completions":
		return true
	default:
		return false
	}
}

// 跟踪编号必须早于鉴权、路由和参数校验生成，失败请求也能关联日志。
func requestTrace(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tracked := &responseLogWriter{ResponseWriter: w}
		defer func() {
			status := tracked.status
			if status == 0 {
				status = http.StatusOK
			}
			slog.Info("[AIRelay] request completed", "method", r.Method, "path", r.URL.Path, "status", status, "request_id", tracked.Header().Get("X-Request-ID"))
		}()
		w = tracked
		id := newRequestID()
		w.Header().Set("X-Request-ID", id)
		if supplied := r.Header.Get("X-Request-ID"); supplied != "" {
			if !validRequestID(supplied) {
				openAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid X-Request-ID")
				return
			}
			id = supplied
		}
		w.Header().Set("X-Request-ID", id)
		next.ServeHTTP(w, r)
	})
}

type responseLogWriter struct {
	http.ResponseWriter
	status int
}

func (w *responseLogWriter) WriteHeader(status int) {
	if w.status != 0 {
		return
	}
	w.status = status
	w.ResponseWriter.WriteHeader(status)
}

func (w *responseLogWriter) Write(data []byte) (int, error) {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	return w.ResponseWriter.Write(data)
}

func (w *responseLogWriter) Flush() {
	if w.status == 0 {
		w.WriteHeader(http.StatusOK)
	}
	if flusher, ok := w.ResponseWriter.(http.Flusher); ok {
		flusher.Flush()
	}
}

func (w *responseLogWriter) Unwrap() http.ResponseWriter { return w.ResponseWriter }

func validRequestID(value string) bool {
	if len(value) == 0 || len(value) > 36 {
		return false
	}
	for _, c := range value {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || strings.ContainsRune("._:-", c)) {
			return false
		}
	}
	return true
}

func agentPageCORS(next http.Handler) http.Handler {
	const agentOrigin = "https://cxec-agent.invalid"
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Origin") != agentOrigin {
			next.ServeHTTP(w, r)
			return
		}
		allowedMethod := ""
		switch r.URL.Path {
		case "/v1/service/config":
			allowedMethod = http.MethodGet
		case "/v1/client/initialize":
			allowedMethod = http.MethodPost
		case "/v1/account/quota":
			allowedMethod = http.MethodGet
		case "/v1/chat/completions":
			allowedMethod = http.MethodPost
		}
		if allowedMethod == "" {
			next.ServeHTTP(w, r)
			return
		}
		w.Header().Add("Vary", "Origin")
		w.Header().Set("Access-Control-Allow-Origin", agentOrigin)
		w.Header().Set("Access-Control-Expose-Headers", "X-Request-ID")
		if r.Method == http.MethodOptions {
			requestedMethod := strings.ToUpper(r.Header.Get("Access-Control-Request-Method"))
			if requestedMethod != allowedMethod {
				w.WriteHeader(http.StatusForbidden)
				return
			}
			for _, header := range strings.Split(r.Header.Get("Access-Control-Request-Headers"), ",") {
				header = strings.TrimSpace(strings.ToLower(header))
				if header != "authorization" && header != "content-type" && header != "x-request-id" {
					w.WriteHeader(http.StatusForbidden)
					return
				}
			}
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
			w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
			w.Header().Set("Access-Control-Max-Age", "600")
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) ederServiceConfig(w http.ResponseWriter, r *http.Request) {
	config, err := s.store.ederServiceConfig(r.Context())
	if err != nil {
		openAIError(w, 503, "service_unavailable", "Eder service configuration unavailable")
		return
	}
	writeSuccess(w, map[string]any{
		"enabled":                  config.Enabled,
		"default_reasoning_effort": config.DefaultReasoningEffort,
		"default_context_tokens":   config.DefaultContextTokens,
		"unavailable_message":      config.UnavailableMessage,
	})
}

type ederClientInfo struct {
	EderVersion     string `json:"eder_version"`
	SoftwareVersion string `json:"software_version"`
	SoftwareHash    string `json:"software_hash"`
	System          string `json:"system"`
}

func (s *Server) initializeClient(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 8<<10)
	var info ederClientInfo
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if decoder.Decode(&info) != nil || !validEderClientInfo(info) {
		openAIError(w, http.StatusBadRequest, "invalid_request_error", "invalid Eder client information")
		return
	}
	config, err := s.store.ederServiceConfig(r.Context())
	if err != nil {
		openAIError(w, http.StatusServiceUnavailable, "service_unavailable", "Eder service configuration unavailable")
		return
	}
	available := config.Enabled && !blockedEderClient(config, info)
	message := ""
	if !available {
		message = ederUnavailableMessage(config)
	}
	writeSuccess(w, map[string]any{"available": available, "message": message})
}

func validEderClientInfo(info ederClientInfo) bool {
	if info.EderVersion == "" || len(info.EderVersion) > 64 || info.SoftwareVersion == "" || len(info.SoftwareVersion) > 64 || len(info.System) == 0 || len(info.System) > 256 || len(info.SoftwareHash) != 64 {
		return false
	}
	_, err := hex.DecodeString(info.SoftwareHash)
	return err == nil
}

func validBlockedVersions(versions []string) bool {
	seen := make(map[string]struct{}, len(versions))
	for _, version := range versions {
		if len(version) == 0 || len(version) > 64 {
			return false
		}
		for i, char := range version {
			if !((char >= 'A' && char <= 'Z') || (char >= 'a' && char <= 'z') || (char >= '0' && char <= '9') || (i > 0 && (char == '.' || char == '_' || char == '+' || char == '-'))) {
				return false
			}
		}
		if _, exists := seen[version]; exists {
			return false
		}
		seen[version] = struct{}{}
	}
	return len(versions) <= 200
}

func blockedEderClient(config EderServiceConfig, info ederClientInfo) bool {
	for _, version := range config.BlockedEderVersions {
		if info.EderVersion == version {
			return true
		}
	}
	for _, version := range config.BlockedSoftwareVersions {
		if info.SoftwareVersion == version {
			return true
		}
	}
	return false
}

func ederUnavailableMessage(config EderServiceConfig) string {
	if strings.TrimSpace(config.UnavailableMessage) != "" {
		return config.UnavailableMessage
	}
	return "当前Eder不可用，可能版本过低，请您升级软件后再试。如有问题请联系客服。"
}

func (s *Server) member(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if len(s.cfg.JWTSecret) < 32 {
			openAIError(w, 503, "service_unavailable", "member authentication is not configured")
			return
		}
		parts := strings.Fields(r.Header.Get("Authorization"))
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			openAIError(w, 401, "authentication_error", "bearer token required")
			return
		}
		claims, err := validateJWT(parts[1], s.cfg)
		if err != nil {
			openAIError(w, 401, "authentication_error", "invalid or expired bearer token")
			return
		}
		var status string
		var expiry time.Time
		err = s.store.db.QueryRowContext(r.Context(), `SELECT a.status,t.expires_at FROM ai_user_token t JOIN ai_account a ON a.uid=t.uid WHERE t.jti=? AND t.uid=? AND t.revoked_at IS NULL`, claims.TokenID, claims.Subject).Scan(&status, &expiry)
		if err != nil || (status != "active" && status != "suspended") || !expiry.After(time.Now()) {
			openAIError(w, 401, "authentication_error", "token is revoked or account is unavailable")
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), memberKey{}, claims)))
	})
}
func (s *Server) admin(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
		if s.cfg.AdminToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(s.cfg.AdminToken)) != 1 {
			openAIError(w, 401, "authentication_error", "internal authorization required")
			return
		}
		next.ServeHTTP(w, r)
	})
}

type memberKey struct{}

func claimsOf(r *http.Request) Claims { return r.Context().Value(memberKey{}).(Claims) }
func (s *Server) registerToken(w http.ResponseWriter, r *http.Request) {
	var v struct {
		JTI     string `json:"jti"`
		UID     string `json:"uid"`
		Product string `json:"product"`
		Expires int64  `json:"expires_at"`
	}
	now := time.Now().Unix()
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&v) != nil || v.JTI == "" || v.UID == "" || v.Expires <= now || v.Expires > now+maxJWTLifetimeSeconds {
		openAIError(w, 400, "invalid_request_error", "invalid token registration")
		return
	}
	if v.Product == "" {
		v.Product = "17"
	}
	ctx := r.Context()
	tx, err := s.store.db.BeginTx(ctx, nil)
	if err != nil {
		openAIError(w, 503, "service_unavailable", "token registration database unavailable")
		return
	}
	defer tx.Rollback()
	// The EC member UID in the trusted token-registration request is the AI
	// account key. These idempotent inserts initialize first-time users without
	// resetting an existing account status or wallet balance.
	if _, err = tx.ExecContext(ctx, `INSERT INTO ai_account(uid) VALUES(?) ON DUPLICATE KEY UPDATE uid=VALUES(uid)`, v.UID); err != nil {
		openAIError(w, 503, "service_unavailable", "account initialization failed")
		return
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO ai_wallet(uid) VALUES(?) ON DUPLICATE KEY UPDATE uid=VALUES(uid)`, v.UID); err != nil {
		openAIError(w, 503, "service_unavailable", "wallet initialization failed")
		return
	}
	_, err = tx.ExecContext(ctx, `INSERT INTO ai_user_token(jti,uid,product,expires_at) VALUES(?,?,?,FROM_UNIXTIME(?)) ON DUPLICATE KEY UPDATE uid=VALUES(uid),product=VALUES(product),expires_at=VALUES(expires_at),revoked_at=NULL`, v.JTI, v.UID, v.Product, v.Expires)
	if err != nil {
		openAIError(w, 503, "service_unavailable", "token registration failed")
		return
	}
	if tx.Commit() != nil {
		openAIError(w, 503, "service_unavailable", "token registration commit failed")
		return
	}
	writeSuccess(w, map[string]any{"ok": true})
}
func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	c := claimsOf(r)
	data, err := s.publicModelPrices(r.Context(), c.Product)
	if err != nil {
		openAIError(w, 503, "service_unavailable", "model pricing catalog unavailable")
		return
	}
	writeSuccess(w, map[string]any{"object": "list", "data": data})
}
func (s *Server) quota(w http.ResponseWriter, r *http.Request) {
	c := claimsOf(r)
	snapshot, err := s.quotaSnapshot(r.Context(), c.Subject, parentUID(r.Context(), c.Subject))
	if err != nil {
		openAIError(w, 503, "service_unavailable", "quota unavailable")
		return
	}
	models, err := s.publicModelPrices(r.Context(), c.Product)
	if err != nil {
		openAIError(w, 503, "service_unavailable", "quota model pricing unavailable")
		return
	}
	writeSuccess(w, map[string]any{"uid": c.Subject, "status": snapshot.status, "disabled_message": snapshot.disabledMessage, "balance_credit": formatCredit(snapshot.self + snapshot.parent), "self_balance_credit": formatCredit(snapshot.self), "parent_balance_credit": formatCredit(snapshot.parent), "quota_baseline_credit": snapshot.quotaBaselineCredit, "remaining_percent": snapshot.remainingPercent, "total_credit": snapshot.totalCredit, "used_credit": snapshot.usedCredit, "used_percent": snapshot.usedPercent, "unit": "credit", "pricing_models": models, "default_model": defaultChatModelID(models)})
}
func (s *Server) usage(w http.ResponseWriter, r *http.Request) {
	c := claimsOf(r)
	rows, err := s.store.db.QueryContext(r.Context(), `SELECT request_id,model_id,prompt_tokens,cached_prompt_tokens,completion_tokens,charged_micro,status,created_at FROM ai_usage WHERE uid=? ORDER BY usage_id DESC LIMIT 100`, c.Subject)
	if err != nil {
		openAIError(w, 503, "service_unavailable", "usage unavailable")
		return
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, model, status string
		var p, cached, o, cost int64
		var at time.Time
		if rows.Scan(&id, &model, &p, &cached, &o, &cost, &status, &at) == nil {
			out = append(out, map[string]any{"request_id": id, "model": model, "prompt_tokens": p, "cached_prompt_tokens": cached, "completion_tokens": o, "charged_credit": formatCredit(cost), "status": status, "created_at": at})
		}
	}
	writeSuccess(w, out)
}

type chatRequest struct {
	Model               string           `json:"model"`
	Stream              bool             `json:"stream"`
	Messages            []map[string]any `json:"messages"`
	Tools               json.RawMessage  `json:"tools"`
	ToolChoice          json.RawMessage  `json:"tool_choice"`
	MaxTokens           int              `json:"max_tokens"`
	MaxCompletionTokens int              `json:"max_completion_tokens"`
	Thinking            json.RawMessage  `json:"thinking"`
	ReasoningEffort     string           `json:"reasoning_effort"`
	Temperature         *float64         `json:"temperature"`
	TopP                *float64         `json:"top_p"`
	Stop                json.RawMessage  `json:"stop"`
	User                string           `json:"user"`
	ClientInfo          *ederClientInfo  `json:"client_info"`
}
type modelRow struct {
	provider, baseURL, upstream            string
	modelType                              string
	credential                             []byte
	inputPrice, outputPrice                int64
	multiplier                             uint64
	tpm, rpm, maxOutput, maxContext        int
	maxImageBytes                          int
	maxImages                              int
	allowThinking, allowTools, allowImages bool
	reasoningEfforts, reasoningFormat      string
	pricing                                modelPricing
}

func (s *Server) chat(w http.ResponseWriter, r *http.Request) {
	started := time.Now()
	failure := &chatFailure{stage: "validation"}
	tracked := &chatFailureWriter{ResponseWriter: w, failure: failure}
	w = tracked
	var req chatRequest
	defer func() { s.saveChatFailure(r, tracked, &req, failure, started) }()
	r.Body = http.MaxBytesReader(w, r.Body, 64<<20)
	dec := json.NewDecoder(r.Body)
	if dec.Decode(&req) != nil || req.Model == "" || len(req.Messages) == 0 {
		openAIError(w, 400, "invalid_request_error", "invalid chat request")
		return
	}
	claims := claimsOf(r)
	var accountStatus, disabledMessage string
	if err := s.store.db.QueryRowContext(r.Context(), `SELECT status,disabled_message FROM ai_account WHERE uid=?`, claims.Subject).Scan(&accountStatus, &disabledMessage); err != nil {
		openAIError(w, http.StatusServiceUnavailable, "service_unavailable", "AI account status unavailable")
		return
	}
	if accountStatus == "suspended" {
		openAIError(w, http.StatusForbidden, "account_disabled", accountDisabledMessage(disabledMessage))
		return
	}
	serviceConfig, err := s.store.ederServiceConfig(r.Context())
	if err != nil {
		openAIError(w, http.StatusServiceUnavailable, "service_unavailable", "Eder service configuration unavailable")
		return
	}
	if !serviceConfig.Enabled || req.ClientInfo == nil || !validEderClientInfo(*req.ClientInfo) || blockedEderClient(serviceConfig, *req.ClientInfo) {
		openAIError(w, http.StatusForbidden, "service_disabled", ederUnavailableMessage(serviceConfig))
		return
	}
	// 在上下文估算与预扣前注入，保证全局规则参与限额和计费。
	req.Messages = applyGlobalPrompt(req.Messages, serviceConfig.GlobalPrompt)
	var m modelRow
	err = s.store.db.QueryRowContext(r.Context(), `SELECT p.name,p.base_url,m.upstream_model,m.model_type,c.secret_value,m.input_price_micro_per_1k,m.cache_input_price_micro_per_1k,m.output_price_micro_per_1k,m.multiplier_bps,m.tpm,m.rpm,m.max_output_tokens,m.max_context_tokens,m.allow_thinking,m.allow_tools,m.allow_images,m.max_image_bytes,m.max_images_per_request,m.reasoning_efforts,m.reasoning_format,s.time_zone,s.peak_enabled,s.peak_start,s.peak_end,s.peak_input_price_micro_per_1k,s.peak_cache_input_price_micro_per_1k,s.peak_output_price_micro_per_1k,s.peak_multiplier_bps,s.limited_enabled,s.limited_start_unix,s.limited_end_unix,s.limited_input_price_micro_per_1k,s.limited_cache_input_price_micro_per_1k,s.limited_output_price_micro_per_1k,s.limited_multiplier_bps,s.vision_input_price_micro_per_1k,s.vision_cache_input_price_micro_per_1k,s.vision_output_price_micro_per_1k,s.peak_vision_input_price_micro_per_1k,s.peak_vision_cache_input_price_micro_per_1k,s.peak_vision_output_price_micro_per_1k,s.limited_vision_input_price_micro_per_1k,s.limited_vision_cache_input_price_micro_per_1k,s.limited_vision_output_price_micro_per_1k FROM ai_model m JOIN ai_model_pricing s ON s.model_id=m.model_id JOIN ai_provider p ON p.provider_id=m.provider_id AND p.enabled=1 JOIN ai_provider_credential c ON c.credential_id=m.credential_id AND c.enabled=1 WHERE m.model_id=? AND m.enabled=1 AND (m.product_scope='' OR m.product_scope=?)`, req.Model, claims.Product).Scan(&m.provider, &m.baseURL, &m.upstream, &m.modelType, &m.credential, &m.inputPrice, &m.pricing.cacheInput, &m.outputPrice, &m.multiplier, &m.tpm, &m.rpm, &m.maxOutput, &m.maxContext, &m.allowThinking, &m.allowTools, &m.allowImages, &m.maxImageBytes, &m.maxImages, &m.reasoningEfforts, &m.reasoningFormat, &m.pricing.timeZone, &m.pricing.peakEnabled, &m.pricing.peakStart, &m.pricing.peakEnd, &m.pricing.peakInput, &m.pricing.peakCacheInput, &m.pricing.peakOutput, &m.pricing.peakMultiplier, &m.pricing.limitedEnabled, &m.pricing.limitedStart, &m.pricing.limitedEnd, &m.pricing.limitedInput, &m.pricing.limitedCacheInput, &m.pricing.limitedOutput, &m.pricing.limitedMultiplier, &m.pricing.visionInput, &m.pricing.visionCacheInput, &m.pricing.visionOutput, &m.pricing.peakVisionInput, &m.pricing.peakVisionCacheInput, &m.pricing.peakVisionOutput, &m.pricing.limitedVisionInput, &m.pricing.limitedVisionCacheInput, &m.pricing.limitedVisionOutput)
	if err != nil {
		openAIError(w, 404, "model_not_found", "model is unavailable")
		return
	}
	failure.provider = m.provider
	failure.upstreamModel = m.upstream
	failure.stage = "model_policy"
	if !validModelType(m.modelType) {
		openAIError(w, 503, "service_unavailable", "model type configuration is invalid")
		return
	}
	// 非文本模型目前没有对应聊天执行器，必须在锁价和额度预扣前拒绝。
	if m.modelType != modelTypeText {
		openAIError(w, 400, "invalid_request_error", "only text models are available for chat")
		return
	}
	if !m.allowTools && (len(req.Tools) > 0 && string(req.Tools) != "null" && string(req.Tools) != "[]" || len(req.ToolChoice) > 0 && string(req.ToolChoice) != "null" && string(req.ToolChoice) != "") {
		openAIError(w, 403, "permission_error", "tool calling is disabled for this model")
		return
	}
	allowedEfforts, policyErr := reasoningPolicy(m.reasoningEfforts, m.reasoningFormat, m.allowThinking)
	if policyErr != nil {
		openAIError(w, 503, "service_unavailable", "model reasoning configuration is invalid")
		return
	}
	// 未指定思考参数时采用服务默认，显式个人选择仍由模型白名单验证。
	if req.ReasoningEffort == "" && (len(req.Thinking) == 0 || string(req.Thinking) == "null") {
		req.ReasoningEffort, err = defaultReasoningEffort(serviceConfig.DefaultReasoningEffort, allowedEfforts)
		if err != nil {
			openAIError(w, 503, "service_unavailable", "invalid Eder chat defaults")
			return
		}
	}
	effort, reasoningErr := selectReasoning(req, allowedEfforts)
	if reasoningErr != nil {
		openAIError(w, 403, "permission_error", "the requested reasoning setting is not enabled for this model")
		return
	}
	maxOut := req.MaxCompletionTokens
	if maxOut <= 0 {
		maxOut = req.MaxTokens
	}
	if maxOut <= 0 || maxOut > m.maxOutput {
		maxOut = m.maxOutput
	}
	req.MaxTokens = maxOut
	req.MaxCompletionTokens = 0
	if !validModelImageCount(m.maxImages) {
		openAIError(w, 503, "service_unavailable", "invalid model image count limit")
		return
	}
	if !validModelImageLimit(m.maxImageBytes) {
		openAIError(w, 503, "service_unavailable", "invalid model image size limit")
		return
	}
	imagePolicy := modelImageInputPolicy(m.baseURL, m.upstream)
	imagePolicy.maxFileBytes = m.maxImageBytes
	imagePolicy.maxImages = m.maxImages
	inEstimate, imageErr := chatInputEstimate(req.Messages, req.Tools, req.ToolChoice, m.allowImages, imagePolicy)
	if imageErr != nil {
		openAIError(w, http.StatusBadRequest, "invalid_request_error", imageErr.Error())
		return
	}
	rate, err := m.pricing.resolveForInput(time.Now(), m.inputPrice, m.outputPrice, m.multiplier, hasImageInput(req.Messages))
	if err != nil {
		openAIError(w, 503, "service_unavailable", "model pricing configuration is invalid")
		return
	}
	if inEstimate < 1 {
		inEstimate = 1
	}
	// 上下文包含输入与预留输出，先限制模型窗口再应用每分钟额度。
	if !validContextLimit(m.maxContext) {
		openAIError(w, 503, "service_unavailable", "model context configuration is invalid")
		return
	}
	if inEstimate >= int64(m.maxContext) {
		openAIError(w, 400, "invalid_request_error", "estimated prompt size exceeds this model's context limit")
		return
	}
	if remaining := m.maxContext - int(inEstimate); maxOut > remaining {
		maxOut = remaining
		req.MaxTokens = maxOut
	}
	if m.tpm > 0 {
		if inEstimate >= int64(m.tpm) {
			openAIError(w, 400, "invalid_request_error", "estimated prompt size exceeds this model's TPM limit")
			return
		}
		maxForTPM := int(int64(m.tpm) - inEstimate)
		if maxOut > maxForTPM {
			maxOut = maxForTPM
			req.MaxTokens = maxOut
		}
	}
	requestID := r.Header.Get("Idempotency-Key")
	if requestID == "" {
		requestID = w.Header().Get("X-Request-ID")
	}
	if len(requestID) > 36 {
		openAIError(w, 400, "invalid_request_error", "Idempotency-Key must be at most 36 characters")
		return
	}
	if !validRequestID(requestID) {
		openAIError(w, 400, "invalid_request_error", "invalid Idempotency-Key")
		return
	}
	w.Header().Set("X-Request-ID", requestID)
	reserveInputRate := rate.input
	if rate.cacheInput > reserveInputRate {
		reserveInputRate = rate.cacheInput
	}
	reservation, err := price(inEstimate, int64(maxOut), reserveInputRate, rate.output, rate.multiplier)
	if err != nil {
		openAIError(w, 503, "service_unavailable", "credit reservation calculation failed")
		return
	}
	reservedTokens := inEstimate + int64(maxOut)
	failure.stage = "queue_reservation"
	if err = s.reserveQueued(r.Context(), claims.Subject, requestID, req.Model, reservation, m.rpm, m.tpm, reservedTokens); err != nil {
		failure.err = err
		if errors.Is(err, errQuota) || errors.Is(err, errAccountWalletUnavailable) {
			var status, message string
			if lookupErr := s.store.db.QueryRowContext(r.Context(), `SELECT status,disabled_message FROM ai_account WHERE uid=?`, claims.Subject).Scan(&status, &message); lookupErr != nil {
				openAIError(w, http.StatusServiceUnavailable, "service_unavailable", "AI account status unavailable")
				return
			}
			if status == "suspended" {
				openAIError(w, http.StatusForbidden, "account_disabled", accountDisabledMessage(message))
				return
			}
			if errors.Is(err, errAccountWalletUnavailable) {
				openAIError(w, http.StatusServiceUnavailable, "service_unavailable", "account wallet unavailable")
				return
			}
			openAIError(w, 402, "insufficient_quota", "insufficient AI quota")
		} else if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return
		} else {
			openAIError(w, 503, "service_unavailable", "quota reservation failed")
		}
		return
	}
	key, err := decryptCredential(s.cfg.EncryptionKey, m.credential)
	if err != nil {
		failure.stage = "provider_credential"
		failure.err = err
		s.release(r.Context(), claims.Subject, requestID, reservation)
		openAIError(w, 503, "service_unavailable", "provider credential configuration error")
		return
	}
	failure.secret = key
	failure.stage = "upstream_request"
	body := map[string]any{"model": m.upstream, "stream": req.Stream, "messages": req.Messages, "max_tokens": maxOut}
	if req.Stream {
		body["stream_options"] = map[string]any{"include_usage": true}
	}
	if req.Tools != nil {
		body["tools"] = req.Tools
	}
	if req.ToolChoice != nil {
		body["tool_choice"] = req.ToolChoice
	}
	if req.Temperature != nil {
		body["temperature"] = *req.Temperature
	}
	if req.TopP != nil {
		body["top_p"] = *req.TopP
	}
	if req.Stop != nil {
		body["stop"] = req.Stop
	}
	applyReasoning(body, m.reasoningFormat, effort)

	raw, _ := json.Marshal(body)
	ctx, cancel := context.WithTimeout(r.Context(), s.cfg.UpstreamTimeout)
	defer cancel()
	u := strings.TrimRight(m.baseURL, "/")
	if !strings.HasSuffix(u, "/chat/completions") {
		u += "/chat/completions"
	}
	up, err := http.NewRequestWithContext(ctx, http.MethodPost, u, strings.NewReader(string(raw)))
	if err != nil {
		failure.err = err
		s.release(r.Context(), claims.Subject, requestID, reservation)
		openAIError(w, 502, "upstream_error", "provider request failed")
		return
	}
	up.Header.Set("Content-Type", "application/json")
	up.Header.Set("Authorization", "Bearer "+key)
	if req.Stream {
		// 流式输出避免压缩器积攒小块数据后才发送。
		up.Header.Set("Accept-Encoding", "identity")
	}
	resp, err := s.client.Do(up)
	if err != nil {
		failure.err = err
		s.release(r.Context(), claims.Subject, requestID, reservation)
		openAIError(w, 502, "upstream_error", "provider request failed")
		return
	}
	defer resp.Body.Close()
	failure.upstreamStatus = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		diagnostic, readErr := io.ReadAll(io.LimitReader(resp.Body, diagnosticLimit+1))
		failure.truncated = len(diagnostic) > diagnosticLimit
		if failure.truncated {
			diagnostic = diagnostic[:diagnosticLimit]
		}
		failure.upstreamBody = string(diagnostic)
		failure.err = readErr
		s.release(r.Context(), claims.Subject, requestID, reservation)
		openAIError(w, 502, "upstream_error", fmt.Sprintf("provider returned status %d", resp.StatusCode))
		return
	}
	var prompt, cached, completion int64
	var proxyErr error
	failure.stage = "upstream_response"
	if req.Stream {
		prompt, cached, completion, proxyErr = proxySSE(w, resp)
	} else {
		var data []byte
		data, proxyErr = io.ReadAll(io.LimitReader(resp.Body, (8<<20)+1))
		rawResponse := data
		if proxyErr == nil && len(data) > 8<<20 {
			proxyErr = errors.New("provider response too large")
		}
		if proxyErr == nil {
			data, prompt, completion, cached, proxyErr = safeCompletionFrame(data)
		}
		if proxyErr != nil {
			failure.err = proxyErr
			failure.capture(rawResponse)
			s.release(context.Background(), claims.Subject, requestID, reservation)
			openAIError(w, 502, "upstream_error", "invalid upstream completion response")
			return
		}
		if proxyErr == nil {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			_, proxyErr = w.Write(data)
		}
	}
	if proxyErr != nil {
		failure.err = proxyErr
		s.release(context.Background(), claims.Subject, requestID, reservation)
		return
	}
	if prompt == 0 {
		prompt = inEstimate
	}
	if completion == 0 {
		completion = int64(maxOut) / 2
	}
	rate.cachedTokens = cached
	actual, err := priceWithCache(prompt, cached, completion, rate.input, rate.cacheInput, rate.output, rate.multiplier)
	if err != nil {
		slog.Error("[AIRelay] credit settlement calculation failed", "request_id", requestID, "error", err)
		failure.stage = "settlement"
		failure.err = err
		return
	}
	if err = s.settle(context.Background(), claims.Subject, requestID, req.Model, prompt, completion, reservation, actual, rate); err != nil {
		failure.stage = "settlement"
		failure.err = err
		slog.Error("[AIRelay] wallet settlement failed", "request_id", requestID, "error", err)
		return
	}
}

func proxySSE(w http.ResponseWriter, resp *http.Response) (int64, int64, int64, error) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(resp.StatusCode)
	f, ok := w.(http.Flusher)
	if !ok {
		return 0, 0, 0, errors.New("streaming unsupported")
	}
	// 及时交付上游已接受请求的响应头，不等待第一个模型内容事件。
	f.Flush()
	scan := bufio.NewScanner(resp.Body)
	scan.Buffer(make([]byte, 4096), 2<<20)
	var in, cached, out int64
	var dataLines []string
	var eventSize int
	fail := func(err error) (int64, int64, int64, error) {
		slog.Warn("[AIRelay] upstream stream failed", "reason", err.Error(), "request_id", w.Header().Get("X-Request-ID"))
		requestID := w.Header().Get("X-Request-ID")
		message := "AI服务暂时无法使用，请稍后重试。"
		payload, _ := json.Marshal(map[string]any{
			"code":       http.StatusBadGateway,
			"request_id": requestID,
			"msg":        message,
			"data":       nil,
			"errors":     nil,
			"error":      map[string]string{"type": "upstream_error", "message": message, "request_id": requestID},
		})
		fmt.Fprintf(w, "data: %s\n\n", payload)
		f.Flush()
		return in, cached, out, err
	}
	consume := func() (bool, error) {
		payload := strings.Join(dataLines, "\n")
		dataLines = nil
		eventSize = 0
		if payload == "[DONE]" {
			_, err := io.WriteString(w, "data: [DONE]\n\n")
			f.Flush()
			return true, err
		}
		cleaned, prompt, completion, cacheTokens, err := safeCompletionFrame([]byte(payload))
		if err != nil {
			if tracked, ok := w.(*chatFailureWriter); ok {
				tracked.failure.capture([]byte(payload))
			}
			return false, err
		}
		if prompt > 0 {
			in = prompt
		}
		if completion > 0 {
			out = completion
		}
		if cacheTokens > 0 {
			cached = cacheTokens
		}
		_, err = fmt.Fprintf(w, "data: %s\n\n", cleaned)
		f.Flush()
		return false, err
	}
	for scan.Scan() {
		line := scan.Text()
		if strings.HasPrefix(line, "data:") {
			part := strings.TrimPrefix(strings.TrimPrefix(line, "data:"), " ")
			eventSize += len(part)
			if eventSize > 2<<20 {
				return fail(errors.New("upstream event exceeded limit"))
			}
			dataLines = append(dataLines, part)
		} else if strings.HasPrefix(line, ":") {
			// 只下发固定心跳，既保持流活跃，也不透传上游注释里的私有信息。
			if _, err := io.WriteString(w, ": keep-alive\n\n"); err != nil {
				return in, cached, out, err
			}
			f.Flush()
		} else if line == "" && len(dataLines) > 0 {
			done, err := consume()
			if err != nil {
				return fail(err)
			}
			if done {
				return in, cached, out, nil
			}
		}
	}
	if err := scan.Err(); err != nil {
		return fail(err)
	}
	if len(dataLines) > 0 {
		done, err := consume()
		if err != nil {
			return fail(err)
		}
		if done {
			return in, cached, out, nil
		}
	}
	_, _, _, err := fail(io.ErrUnexpectedEOF)
	return in, cached, out, err
}

var errQuota = errors.New("insufficient quota")
var errRate = errors.New("rate limited")

func (s *Server) reserveQueued(ctx context.Context, uid, requestID, model string, amount int64, rpm, tpm int, estimated int64) error {
	for {
		err := s.reserve(ctx, uid, requestID, model, amount, rpm, tpm, estimated)
		if !errors.Is(err, errRate) {
			return err
		}
		if err = s.waitForRateWindow(ctx); err != nil {
			return err
		}
	}
}

func (s *Server) waitForRateWindow(ctx context.Context) error {
	var waitMicros int64
	err := s.store.db.QueryRowContext(ctx, `SELECT GREATEST(1000,TIMESTAMPDIFF(MICROSECOND,NOW(),DATE_ADD(DATE_FORMAT(NOW(),'%Y-%m-%d %H:%i:00'),INTERVAL 1 MINUTE)))`).Scan(&waitMicros)
	if err != nil {
		return err
	}
	if waitMicros < 1000 {
		waitMicros = 1000
	}
	timer := time.NewTimer(time.Duration(waitMicros)*time.Microsecond + 10*time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *Server) reserve(ctx context.Context, uid, requestID, model string, amount int64, rpm, tpm int, estimated int64) error {
	return s.reserveWithRecord(ctx, uid, requestID, model, amount, rpm, tpm, estimated, nil)
}

func (s *Server) reserveWithRecord(ctx context.Context, uid, requestID, model string, amount int64, rpm, tpm int, estimated int64, record func(*sql.Tx) error) error {
	tx, e := s.store.db.BeginTx(ctx, nil)
	if e != nil {
		return e
	}
	defer tx.Rollback()
	parts, e := lockWallets(ctx, tx, uid, parentUID(ctx, uid))
	if e != nil {
		return e
	}
	if e = allocateWallet(parts, amount); e != nil {
		return e
	}
	// uid=0 is the shared model bucket, so TPM/RPM policies apply across all
	// users and all relay instances connected to this database.
	if _, e = tx.ExecContext(ctx, `INSERT INTO ai_rate_window(uid,model_id,window_minute,request_count,token_count) VALUES(0,?,DATE_FORMAT(NOW(),'%Y-%m-%d %H:%i:00'),0,0) ON DUPLICATE KEY UPDATE uid=VALUES(uid)`, model); e != nil {
		return e
	}
	var requests int
	var tokens int64
	if e = tx.QueryRowContext(ctx, `SELECT request_count,token_count FROM ai_rate_window WHERE uid=0 AND model_id=? AND window_minute=DATE_FORMAT(NOW(),'%Y-%m-%d %H:%i:00') FOR UPDATE`, model).Scan(&requests, &tokens); e != nil {
		return e
	}
	if rpm > 0 && requests >= rpm || tpm > 0 && tokens+estimated > int64(tpm) {
		return errRate
	}
	if _, e = tx.ExecContext(ctx, `UPDATE ai_rate_window SET request_count=request_count+1,token_count=token_count+? WHERE uid=0 AND model_id=? AND window_minute=DATE_FORMAT(NOW(),'%Y-%m-%d %H:%i:00')`, estimated, model); e != nil {
		return e
	}
	// 即便主账号预扣为零，也记录原共享钱包，结算不得转用变更后的主账号。
	for i, p := range parts {
		if _, e = tx.ExecContext(ctx, `UPDATE ai_wallet SET balance_micro=balance_micro-? WHERE uid=?`, p.charge, p.uid); e != nil {
			return e
		}
		reason := "reserve"
		if i > 0 {
			reason = "reserve_parent"
		}
		if _, e = tx.ExecContext(ctx, `INSERT INTO ai_wallet_ledger(uid,request_id,delta_micro,balance_after_micro,reason) VALUES(?,?,?,?,?)`, p.uid, requestID, -p.charge, p.balance-p.charge, reason); e != nil {
			return e
		}
	}
	if record != nil {
		if e = record(tx); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func (s *Server) settle(ctx context.Context, uid, requestID, model string, prompt, completion, reservation, actual int64, rate billingRate) error {
	return s.settleWithRecord(ctx, uid, requestID, model, prompt, completion, reservation, actual, rate, nil)
}

func (s *Server) settleWithRecord(ctx context.Context, uid, requestID, model string, prompt, completion, reservation, actual int64, rate billingRate, record func(*sql.Tx) error) error {
	tx, e := s.store.db.BeginTx(ctx, &sql.TxOptions{Isolation: sql.LevelReadCommitted})
	if e != nil {
		return e
	}
	defer tx.Rollback()
	parts, e := reservedWallets(ctx, tx, uid, requestID, reservation)
	if e != nil {
		return e
	}
	if e = allocateWallet(parts, actual); e != nil {
		return e
	}
	if e = writeWalletResult(ctx, tx, parts, requestID, "settlement"); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO ai_usage(request_id,uid,model_id,prompt_tokens,cached_prompt_tokens,completion_tokens,charged_micro,status) VALUES(?,?,?,?,?,?,?,'completed')`, requestID, uid, model, prompt, rate.cachedTokens, completion, actual); e != nil {
		return e
	}
	if _, e = tx.ExecContext(ctx, `INSERT INTO ai_usage_pricing(request_id,uid,model_id,price_kind,billing_input_kind,time_zone,price_locked_at,input_price_micro_per_1k,cache_input_price_micro_per_1k,output_price_micro_per_1k,multiplier_bps) VALUES(?,?,?,?,?,?,?,?,?,?,?)`, requestID, uid, model, rate.kind, rate.inputKind(), rate.timeZone, rate.lockedAt.Format("2006-01-02 15:04:05.000000"), rate.input, rate.cacheInput, rate.output, rate.multiplier); e != nil {
		return e
	}
	if rate.generationKind != "" {
		if _, e = tx.ExecContext(ctx, `UPDATE ai_usage_pricing SET generation_units=?,generation_unit_price_micro=? WHERE request_id=?`, rate.generationUnits, rate.generationPrice, requestID); e != nil {
			return e
		}
	}
	if record != nil {
		if e = record(tx); e != nil {
			return e
		}
	}
	return tx.Commit()
}
func newRequestID() string {
	b := make([]byte, 16)
	if _, e := rand.Read(b); e != nil {
		return fmt.Sprintf("%032x", time.Now().UnixNano())
	}
	h := hex.EncodeToString(b)
	return h[:8] + "-" + h[8:12] + "-4" + h[13:16] + "-a" + h[17:20] + "-" + h[20:]
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// 普通 JSON 接口统一采用与 CxWebServer ResponseService 相同的 envelope。
func writeSuccess(w http.ResponseWriter, data any) {
	writeJSON(w, http.StatusOK, map[string]any{"code": 0, "msg": "操作成功", "data": data, "errors": nil})
}

// 账户停用提示明确标识账户异常，后台说明保持原文。
func accountDisabledMessage(message string) string {
	if message == "" {
		message = "该账户已被禁用，暂时无法继续对话。"
	}
	return "您的账户异常：" + message
}

func openAIError(w http.ResponseWriter, status int, kind, message string) {
	requestID := w.Header().Get("X-Request-ID")
	if strings.TrimSpace(message) == "" {
		message = http.StatusText(status)
	}
	slog.Warn("[AIRelay] request failed", "status", status, "kind", kind, "reason", message, "request_id", requestID)
	// `error` 保留 OpenAI SDK 兼容；同一响应的 `msg` 是统一业务文本，原文不再按状态码改写。
	writeJSON(w, status, map[string]any{
		"code":       status,
		"request_id": requestID,
		"msg":        message,
		"data":       nil,
		"errors":     nil,
		"error":      map[string]string{"message": message, "type": kind, "request_id": requestID},
	})
}
func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Cache-Control", "no-store")
		next.ServeHTTP(w, r)
	})
}
