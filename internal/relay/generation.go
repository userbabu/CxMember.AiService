package relay

import (
	"bytes"
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	_ "image/jpeg"
	_ "image/png"
	"io"
	"log/slog"
	"math"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type generationRequest struct {
	Model      string          `json:"model"`
	Prompt     string          `json:"prompt"`
	Units      int             `json:"units"`
	ClientInfo *ederClientInfo `json:"client_info"`
}
type generationJob struct {
	ID, UID, Model, Kind, Status, API, Base, Upstream, UpstreamID, Hash string
	Secret                                                              []byte
	Units                                                               int
	Price, Reservation                                                  int64
	Rate                                                                billingRate
	Result                                                              sql.NullString
	Created                                                             time.Time
}
type generationAsset struct {
	URL    string `json:"url,omitempty"`
	Base64 string `json:"b64_json,omitempty"`
	MIME   string `json:"mime_type"`
}

func generationEndpoint(base, path string) (string, error) {
	u, e := url.Parse(base)
	if e != nil || u.Host == "" || u.Scheme != "https" && u.Scheme != "http" || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("invalid generation base URL")
	}
	u.Path = strings.TrimSuffix(u.Path, "/") + path
	return u.String(), nil
}
func validGenerationAPI(kind, api string) bool {
	return kind == "image" && (api == "openai-images" || api == "ark-images") || kind == "video" && (api == "openai-video" || api == "ark-video")
}
func generationAmount(unit int64, n int) (int64, error) {
	if unit <= 0 || n < 1 || n > 30 || unit > math.MaxInt64/int64(n) {
		return 0, errors.New("invalid generation price or units")
	}
	return unit * int64(n), nil
}
func (s *Server) createGeneration(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, 64<<10)
	var req generationRequest
	d := json.NewDecoder(r.Body)
	d.DisallowUnknownFields()
	if d.Decode(&req) != nil || strings.TrimSpace(req.Prompt) == "" || len(req.Prompt) > 16000 || req.Model == "" {
		openAIError(w, 400, "invalid_request_error", "invalid generation request")
		return
	}
	c := claimsOf(r)
	config, e := s.store.ederServiceConfig(r.Context())
	if e != nil {
		openAIError(w, 503, "service_unavailable", "Eder configuration unavailable")
		return
	}
	if !config.Enabled || req.ClientInfo == nil || !validEderClientInfo(*req.ClientInfo) || blockedEderClient(config, *req.ClientInfo) {
		openAIError(w, 403, "service_disabled", ederUnavailableMessage(config))
		return
	}
	var accountStatus, disabledMessage string
	if e = s.store.db.QueryRowContext(r.Context(), `SELECT status,disabled_message FROM ai_account WHERE uid=?`, c.Subject).Scan(&accountStatus, &disabledMessage); e != nil {
		openAIError(w, 503, "service_unavailable", "AI account unavailable")
		return
	}
	if accountStatus != "active" {
		openAIError(w, 403, "account_disabled", accountDisabledMessage(disabledMessage))
		return
	}
	id := w.Header().Get("X-Request-ID")
	raw, _ := json.Marshal(struct {
		Model, Prompt string
		Units         int
	}{req.Model, req.Prompt, req.Units})
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:])
	var oldUID, oldHash string
	e = s.store.db.QueryRowContext(r.Context(), `SELECT CAST(uid AS CHAR),request_hash FROM ai_generation WHERE request_id=?`, id).Scan(&oldUID, &oldHash)
	if e == nil {
		if oldUID != c.Subject || oldHash != hash {
			openAIError(w, 409, "idempotency_conflict", "request id already used")
			return
		}
		s.writeGeneration(w, r.Context(), id, c.Subject)
		return
	}
	if !errors.Is(e, sql.ErrNoRows) {
		openAIError(w, 503, "service_unavailable", "generation storage unavailable")
		return
	}
	j := generationJob{ID: id, UID: c.Subject, Model: req.Model, Hash: hash}
	var size string
	var rpm int
	var standard, peak, limited int64
	var p modelPricing
	e = s.store.db.QueryRowContext(r.Context(), `SELECT m.model_type,m.generation_api,m.generation_size,v.base_url,m.upstream_model,c.secret_value,m.rpm,p.generation_price_micro_per_unit,p.peak_generation_price_micro_per_unit,p.limited_generation_price_micro_per_unit,p.time_zone,p.peak_enabled,p.peak_start,p.peak_end,p.limited_enabled,p.limited_start_unix,p.limited_end_unix FROM ai_model m JOIN ai_model_pricing p ON p.model_id=m.model_id JOIN ai_provider v ON v.provider_id=m.provider_id AND v.enabled=1 JOIN ai_provider_credential c ON c.credential_id=m.credential_id AND c.enabled=1 WHERE m.model_id=? AND m.enabled=1 AND (m.product_scope='' OR m.product_scope=?)`, req.Model, c.Product).Scan(&j.Kind, &j.API, &size, &j.Base, &j.Upstream, &j.Secret, &rpm, &standard, &peak, &limited, &p.timeZone, &p.peakEnabled, &p.peakStart, &p.peakEnd, &p.limitedEnabled, &p.limitedStart, &p.limitedEnd)
	if e != nil {
		openAIError(w, 404, "model_not_found", "generation model unavailable")
		return
	}
	if !validGenerationAPI(j.Kind, j.API) || size == "" {
		openAIError(w, 503, "invalid_model_configuration", "generation API and size must be configured")
		return
	}
	if req.Units < 1 || j.Kind == "image" && req.Units > 4 || j.Kind == "video" && (req.Units > 12 || req.Units < 4) || j.API == "openai-video" && req.Units != 4 && req.Units != 8 && req.Units != 12 {
		openAIError(w, 400, "invalid_request_error", "image count must be 1-4; video duration must be supported (4-12 seconds)")
		return
	}
	if j.API == "ark-images" && req.Units != 1 {
		openAIError(w, 400, "invalid_request_error", "Ark images supports one image per request")
		return
	}
	p.peakInput, p.limitedInput = peak, limited
	p.peakMultiplier, p.limitedMultiplier = 10000, 10000
	rate, e := p.resolve(time.Now(), standard, 0, 10000)
	if e != nil {
		openAIError(w, 503, "invalid_model_configuration", "generation time pricing invalid")
		return
	}
	j.Price = rate.input
	j.Units = req.Units
	j.Rate = rate
	amount, e := generationAmount(j.Price, j.Units)
	if e != nil {
		openAIError(w, 503, "invalid_model_configuration", "generation price must be positive")
		return
	}
	j.Reservation = amount
	// 任务与预扣同事务落库，进程中断也不会产生无主冻结余额。
	e = s.reserveWithRecord(r.Context(), c.Subject, id, req.Model, amount, rpm, 0, 0, func(tx *sql.Tx) error {
		_, e := tx.ExecContext(r.Context(), `INSERT INTO ai_generation(request_id,uid,model_id,model_type,request_hash,status,generation_api,base_url,upstream_model,secret_value,requested_units,unit_price_micro,reservation_micro,price_kind,time_zone,price_locked_at) VALUES(?,?,?,?,?,'submitting',?,?,?,?,?,?,?,?,?,?)`, id, c.Subject, req.Model, j.Kind, hash, j.API, j.Base, j.Upstream, j.Secret, j.Units, j.Price, amount, rate.kind, rate.timeZone, rate.lockedAt.Format("2006-01-02 15:04:05.000000"))
		return e
	})
	if e != nil {
		if errors.Is(e, errQuota) {
			openAIError(w, 402, "insufficient_quota", "insufficient credit")
		} else if errors.Is(e, errRate) {
			openAIError(w, 429, "rate_limit_error", "generation RPM limit reached")
		} else {
			openAIError(w, 503, "reservation_failed", "generation reservation failed")
		}
		return
	}
	go s.submitGeneration(j, req.Prompt, size)
	s.writeGeneration(w, r.Context(), id, c.Subject)
}

func (s *Server) generationHTTP(ctx context.Context, j generationJob, method, path string, body io.Reader, contentType string) (*http.Response, error) {
	endpoint, e := generationEndpoint(j.Base, path)
	if e != nil {
		return nil, e
	}
	key, e := decryptCredential(s.cfg.EncryptionKey, j.Secret)
	if e != nil {
		return nil, e
	}
	req, e := http.NewRequestWithContext(ctx, method, endpoint, body)
	if e != nil {
		return nil, e
	}
	req.Header.Set("Authorization", "Bearer "+key)
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	// 禁止带供应商密钥跨重定向发送，也不自动重试付费创建请求。
	client := *s.client
	client.Timeout = 5 * time.Minute
	if method == http.MethodGet && !strings.HasSuffix(path, "/content") {
		client.Timeout = 30 * time.Second
	}
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	return client.Do(req)
}
func readGenerationJSON(resp *http.Response) (map[string]json.RawMessage, error) {
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("generation upstream status %d", resp.StatusCode)
	}
	b, e := io.ReadAll(io.LimitReader(resp.Body, (48<<20)+1))
	if e != nil || len(b) > 48<<20 {
		return nil, errors.New("generation response exceeds limit")
	}
	var data map[string]json.RawMessage
	if json.Unmarshal(b, &data) != nil {
		return nil, errors.New("invalid generation response")
	}
	return data, nil
}
func jsonString(m map[string]json.RawMessage, key string) string {
	var v string
	_ = json.Unmarshal(m[key], &v)
	return v
}
func (s *Server) submitGeneration(j generationJob, prompt, size string) {
	ctx, cancel := context.WithTimeout(context.Background(), 6*time.Minute)
	defer cancel()
	path := "/images/generations"
	contentType := "application/json"
	var body io.Reader
	payload := map[string]any{"model": j.Upstream, "prompt": prompt, "size": size}
	switch j.API {
	case "openai-images":
		payload["n"] = j.Units
	case "ark-images":
		payload["response_format"] = "url"
		payload["sequential_image_generation"] = "disabled"
		if j.Units != 1 {
			s.failGeneration(j, "unsupported_image_count")
			return
		}
	case "ark-video":
		path = "/contents/generations/tasks"
		payload = map[string]any{"model": j.Upstream, "content": []any{map[string]string{"type": "text", "text": prompt}}, "duration": j.Units, "resolution": size, "ratio": "16:9"}
	case "openai-video":
		path = "/videos"
		var b bytes.Buffer
		form := multipart.NewWriter(&b)
		for k, v := range map[string]string{"model": j.Upstream, "prompt": prompt, "seconds": strconv.Itoa(j.Units), "size": size} {
			if e := form.WriteField(k, v); e != nil {
				s.failGeneration(j, "invalid_request")
				return
			}
		}
		_ = form.Close()
		body = &b
		contentType = form.FormDataContentType()
	}
	if body == nil {
		b, _ := json.Marshal(payload)
		body = bytes.NewReader(b)
	}
	resp, e := s.generationHTTP(ctx, j, "POST", path, body, contentType)
	if e != nil {
		s.failGeneration(j, "upstream_unavailable")
		return
	}
	data, e := readGenerationJSON(resp)
	if e != nil {
		s.failGeneration(j, "upstream_rejected")
		return
	}
	if j.Kind == "image" {
		var assets []generationAsset
		if json.Unmarshal(data["data"], &assets) != nil || len(assets) == 0 || len(assets) > j.Units {
			s.failGeneration(j, "invalid_result")
			return
		}
		for i := range assets {
			if !validGenerationAsset(assets[i]) {
				s.failGeneration(j, "invalid_result")
				return
			}
			assets[i].MIME = "image/png"
			if assets[i].Base64 != "" {
				b, _ := base64.StdEncoding.DecodeString(assets[i].Base64)
				_, format, _ := image.DecodeConfig(bytes.NewReader(b))
				assets[i].MIME = "image/" + format
			}
		}
		s.completeGeneration(j, len(assets), assets)
		return
	}
	id := jsonString(data, "id")
	if id == "" || len(id) > 200 || strings.ContainsAny(id, "/?#\\") {
		s.failGeneration(j, "invalid_task_id")
		return
	}
	if _, e = s.store.db.ExecContext(ctx, `UPDATE ai_generation SET status='queued',upstream_id=?,next_poll_at=NOW() WHERE request_id=? AND status='submitting'`, id, j.ID); e != nil {
		slog.Error("[AIGeneration] task persistence failed", "request_id", j.ID, "error", e)
	}
}
func validGenerationAsset(a generationAsset) bool {
	if a.Base64 != "" {
		b, e := base64.StdEncoding.DecodeString(a.Base64)
		if e != nil || len(b) > 8<<20 || a.URL != "" {
			return false
		}
		config, format, e := image.DecodeConfig(bytes.NewReader(b))
		return e == nil && (format == "png" || format == "jpeg") && config.Width > 0 && config.Height > 0 && config.Width <= 16384 && config.Height <= 16384 && int64(config.Width)*int64(config.Height) <= 33554432
	}
	u, e := url.Parse(a.URL)
	return e == nil && u.Scheme == "https" && u.Host != "" && u.User == nil && a.Base64 == ""
}
func (s *Server) completeGeneration(j generationJob, units int, assets []generationAsset) {
	actual, e := generationAmount(j.Price, units)
	if e != nil || actual > j.Reservation {
		s.failGeneration(j, "invalid_result_units")
		return
	}
	raw, e := json.Marshal(assets)
	if e != nil {
		s.failGeneration(j, "invalid_result")
		return
	}
	// 成功结果先持久化；数据库结算失败时由轮询器重试，绝不重复创建付费任务。
	persistCtx, persistCancel := context.WithTimeout(context.Background(), 20*time.Second)
	_, e = s.store.db.ExecContext(persistCtx, `UPDATE ai_generation SET status='queued',actual_units=?,result_json=? WHERE request_id=? AND status IN ('submitting','queued')`, units, string(raw), j.ID)
	persistCancel()
	if e != nil {
		slog.Error("[AIGeneration] result persistence failed", "request_id", j.ID, "error", e)
		return
	}
	rate := j.Rate
	rate.input, rate.cacheInput, rate.output = 0, 0, 0
	rate.multiplier = 10000
	rate.generationKind = j.Kind
	rate.generationPrice = j.Price
	rate.generationUnits = units
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	e = s.settleWithRecord(ctx, j.UID, j.ID, j.Model, 0, 0, j.Reservation, actual, rate, func(tx *sql.Tx) error {
		result, e := tx.ExecContext(ctx, `UPDATE ai_generation SET status='completed',actual_units=?,result_json=?,secret_value=IF(generation_api='openai-video',secret_value,''),lease_until=NULL WHERE request_id=? AND status IN ('submitting','queued')`, units, string(raw), j.ID)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil || n != 1 {
			return errors.New("generation state conflict")
		}
		return nil
	})
	if e != nil {
		slog.Error("[AIGeneration] settlement failed", "request_id", j.ID, "error", e)
	}
}
func (s *Server) failGeneration(j generationJob, code string) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	e := s.refundWithRecord(ctx, j.UID, j.ID, j.Reservation, func(tx *sql.Tx) error {
		result, e := tx.ExecContext(ctx, `UPDATE ai_generation SET status='failed',error_code=?,secret_value='',lease_until=NULL WHERE request_id=? AND status IN ('submitting','queued')`, code, j.ID)
		if e != nil {
			return e
		}
		n, e := result.RowsAffected()
		if e != nil || n != 1 {
			return errors.New("generation state conflict")
		}
		return nil
	})
	if e != nil {
		slog.Error("[AIGeneration] refund failed", "request_id", j.ID, "error", e)
	} else {
		slog.Warn("[AIGeneration] task failed and refunded", "request_id", j.ID, "code", code)
	}
}
func (s *Server) getGeneration(w http.ResponseWriter, r *http.Request) {
	s.writeGeneration(w, r.Context(), r.PathValue("id"), claimsOf(r).Subject)
}

func (s *Server) listGenerations(w http.ResponseWriter, r *http.Request) {
	rows, e := s.store.db.QueryContext(r.Context(), `SELECT request_id,model_id,model_type,status,created_at FROM ai_generation WHERE uid=? ORDER BY created_at DESC LIMIT 20`, claimsOf(r).Subject)
	if e != nil {
		openAIError(w, 503, "service_unavailable", "generation history unavailable")
		return
	}
	defer rows.Close()
	items := []map[string]any{}
	for rows.Next() {
		var id, model, kind, status string
		var created time.Time
		if e = rows.Scan(&id, &model, &kind, &status, &created); e != nil {
			openAIError(w, 503, "service_unavailable", "generation history invalid")
			return
		}
		items = append(items, map[string]any{"generation_id": id, "model": model, "model_type": kind, "status": status, "created_at": created})
	}
	if rows.Err() != nil {
		openAIError(w, 503, "service_unavailable", "generation history unavailable")
		return
	}
	writeSuccess(w, map[string]any{"items": items})
}
func (s *Server) writeGeneration(w http.ResponseWriter, ctx context.Context, id, uid string) {
	var status, kind, model, code string
	var requested int
	var actual sql.NullInt64
	var price int64
	var result sql.NullString
	e := s.store.db.QueryRowContext(ctx, `SELECT status,model_type,model_id,requested_units,actual_units,unit_price_micro,result_json,error_code FROM ai_generation WHERE request_id=? AND uid=?`, id, uid).Scan(&status, &kind, &model, &requested, &actual, &price, &result, &code)
	if errors.Is(e, sql.ErrNoRows) {
		openAIError(w, 404, "not_found", "generation not found")
		return
	}
	if e != nil {
		openAIError(w, 503, "service_unavailable", "generation unavailable")
		return
	}
	assets := []generationAsset{}
	if result.Valid && json.Unmarshal([]byte(result.String), &assets) != nil {
		openAIError(w, 503, "invalid_result", "generation result is invalid")
		return
	}
	charged := int64(0)
	if status == "completed" {
		charged = price * actual.Int64
	}
	writeSuccess(w, map[string]any{"request_id": id, "status": status, "model": model, "model_type": kind, "requested_units": requested, "actual_units": actual.Int64, "unit_price_credit": formatCredit(price), "charged_credit": formatCredit(charged), "assets": assets, "error_code": code})
}

// 后台轮询不依赖客户端连接；客户端取消等待不会遗失已提交的视频任务。
func (s *Server) RunGenerations(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	cleanupAt := time.Time{}
	for {
		if time.Now().After(cleanupAt) {
			if _, e := s.store.db.ExecContext(ctx, `DELETE FROM ai_generation WHERE status IN ('completed','failed') AND updated_at<DATE_SUB(NOW(),INTERVAL 30 DAY) LIMIT 500`); e != nil {
				slog.Error("[AIGeneration] retention failed", "error", e)
			}
			cleanupAt = time.Now().Add(time.Hour)
		}
		s.pollGenerations(ctx)
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}
func (s *Server) pollGenerations(ctx context.Context) {
	rows, e := s.store.db.QueryContext(ctx, `SELECT request_id FROM ai_generation WHERE (status='queued' AND next_poll_at<=NOW() AND (lease_until IS NULL OR lease_until<NOW())) OR (status='submitting' AND created_at<DATE_SUB(NOW(),INTERVAL 15 MINUTE)) ORDER BY created_at LIMIT 8`)
	if e != nil {
		slog.Error("[AIGeneration] poll storage failed", "error", e)
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return
	}
	for _, id := range ids {
		res, e := s.store.db.ExecContext(ctx, `UPDATE ai_generation SET lease_until=DATE_ADD(NOW(),INTERVAL 6 MINUTE),next_poll_at=DATE_ADD(NOW(),INTERVAL 10 SECOND) WHERE request_id=? AND status IN ('queued','submitting') AND (lease_until IS NULL OR lease_until<NOW())`, id)
		if e != nil {
			continue
		}
		n, _ := res.RowsAffected()
		if n != 1 {
			continue
		}
		var j generationJob
		var actual sql.NullInt64
		j.ID = id
		e = s.store.db.QueryRowContext(ctx, `SELECT CAST(uid AS CHAR),model_id,model_type,status,generation_api,base_url,upstream_model,secret_value,upstream_id,requested_units,unit_price_micro,reservation_micro,price_kind,time_zone,price_locked_at,created_at,result_json,actual_units FROM ai_generation WHERE request_id=?`, id).Scan(&j.UID, &j.Model, &j.Kind, &j.Status, &j.API, &j.Base, &j.Upstream, &j.Secret, &j.UpstreamID, &j.Units, &j.Price, &j.Reservation, &j.Rate.kind, &j.Rate.timeZone, &j.Rate.lockedAt, &j.Created, &j.Result, &actual)
		if e != nil {
			continue
		}
		if j.Result.Valid {
			var assets []generationAsset
			if json.Unmarshal([]byte(j.Result.String), &assets) != nil {
				slog.Error("[AIGeneration] stored result invalid", "request_id", j.ID)
				continue
			}
			s.completeGeneration(j, int(actual.Int64), assets)
			continue
		}
		if j.Status == "submitting" || time.Since(j.Created) > 48*time.Hour {
			s.failGeneration(j, "generation_expired")
			continue
		}
		s.pollGeneration(ctx, j)
	}
}
func (s *Server) pollGeneration(ctx context.Context, j generationJob) {
	path := "/contents/generations/tasks/" + url.PathEscape(j.UpstreamID)
	if j.API == "openai-video" {
		path = "/videos/" + url.PathEscape(j.UpstreamID)
	}
	resp, e := s.generationHTTP(ctx, j, "GET", path, nil, "")
	if e != nil {
		s.releaseGenerationLease(ctx, j.ID)
		return
	}
	data, e := readGenerationJSON(resp)
	if e != nil {
		s.releaseGenerationLease(ctx, j.ID)
		return
	}
	status := jsonString(data, "status")
	switch status {
	case "failed", "cancelled", "expired":
		s.failGeneration(j, "upstream_"+status)
	case "completed", "succeeded":
		units := 0
		var asset generationAsset
		if j.API == "openai-video" {
			units, _ = strconv.Atoi(jsonString(data, "seconds"))
			asset = generationAsset{URL: "/v1/generations/" + j.ID + "/content", MIME: "video/mp4"}
		} else {
			_ = json.Unmarshal(data["duration"], &units)
			var content struct {
				VideoURL string `json:"video_url"`
			}
			_ = json.Unmarshal(data["content"], &content)
			asset = generationAsset{URL: content.VideoURL, MIME: "video/mp4"}
			u, e := url.Parse(asset.URL)
			if e != nil || u.Scheme != "https" || u.Host == "" || u.User != nil {
				s.failGeneration(j, "invalid_result")
				return
			}
		}
		s.completeGeneration(j, units, []generationAsset{asset})
	case "queued", "running", "in_progress":
		s.releaseGenerationLease(ctx, j.ID)
	default:
		s.failGeneration(j, "invalid_upstream_status")
	}
}
func (s *Server) releaseGenerationLease(ctx context.Context, id string) {
	if _, e := s.store.db.ExecContext(ctx, `UPDATE ai_generation SET lease_until=NULL WHERE request_id=? AND status='queued'`, id); e != nil {
		slog.Error("[AIGeneration] lease release failed", "request_id", id, "error", e)
	}
}
func (s *Server) generationContent(w http.ResponseWriter, r *http.Request) {
	// 下载仅使用原任务的加密凭据与供应商地址，模型后续改绑不会串用密钥。
	var j generationJob
	var status string
	e := s.store.db.QueryRowContext(r.Context(), `SELECT base_url,upstream_id,secret_value,status FROM ai_generation WHERE request_id=? AND uid=? AND generation_api='openai-video'`, r.PathValue("id"), claimsOf(r).Subject).Scan(&j.Base, &j.UpstreamID, &j.Secret, &status)
	if e != nil || status != "completed" {
		openAIError(w, 404, "not_found", "video content unavailable")
		return
	}
	resp, e := s.generationHTTP(r.Context(), j, "GET", "/videos/"+url.PathEscape(j.UpstreamID)+"/content", nil, "")
	if e != nil {
		openAIError(w, 502, "upstream_error", "video download failed")
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		openAIError(w, 502, "upstream_error", "video download unavailable")
		return
	}
	w.Header().Set("Content-Type", "video/mp4")
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 256<<20))
}
