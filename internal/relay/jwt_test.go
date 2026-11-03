package relay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestOnlyShortLivedMemberJWTsAccepted(t *testing.T) {
	cfg := Config{JWTSecret: strings.Repeat("x", 32), JWTIssuer: "cx-member", JWTAudience: "cx-ai-relay"}
	now := time.Now().Unix()
	valid := Claims{Issuer: cfg.JWTIssuer, Audience: cfg.JWTAudience, Subject: "42", ParentUID: "9007199254740993", Product: "17", TokenID: strings.Repeat("a", 32), IssuedAt: now, NotBefore: now - 5, Expires: now + 900}
	sign := func(c Claims) string {
		header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
		raw, _ := json.Marshal(c)
		unsigned := header + "." + base64.RawURLEncoding.EncodeToString(raw)
		mac := hmac.New(sha256.New, []byte(cfg.JWTSecret))
		mac.Write([]byte(unsigned))
		return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
	}
	if claims, err := validateJWT(sign(valid), cfg); err != nil || claims.ParentUID != valid.ParentUID {
		t.Fatal(err)
	}
	// 主账号 UID 必须包含在签名内，不能通过改写载荷切换共享钱包。
	token := strings.Split(sign(valid), ".")
	tampered := valid
	tampered.ParentUID = "100"
	raw, _ := json.Marshal(tampered)
	token[1] = base64.RawURLEncoding.EncodeToString(raw)
	if _, err := validateJWT(strings.Join(token, "."), cfg); err == nil {
		t.Fatal("tampered parent UID accepted")
	}
	for name, mutate := range map[string]func(*Claims){"long lived": func(c *Claims) { c.Expires = now + 1801 }, "expired": func(c *Claims) { c.Expires = now - 1 }, "missing issue time": func(c *Claims) { c.IssuedAt = 0 }, "wrong product": func(c *Claims) { c.Product = "12" }, "wrong issuer": func(c *Claims) { c.Issuer = "other" }, "wrong audience": func(c *Claims) { c.Audience = "other" }} {
		t.Run(name, func(t *testing.T) {
			c := valid
			mutate(&c)
			if _, err := validateJWT(sign(c), cfg); err == nil {
				t.Fatal("invalid member token accepted")
			}
		})
	}
	if _, err := validateJWT(cfg.JWTSecret, cfg); err == nil {
		t.Fatal("internal secret accepted as a member credential")
	}
	response := httptest.NewRecorder()
	request := httptest.NewRequest("POST", "/internal/v1/tokens", strings.NewReader(fmt.Sprintf(`{"jti":"test","uid":"42","expires_at":%d}`, now+3600)))
	(&Server{}).registerToken(response, request)
	if response.Code != 400 {
		t.Fatalf("long-lived registration accepted: %d", response.Code)
	}
}
