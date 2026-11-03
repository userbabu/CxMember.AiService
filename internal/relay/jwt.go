package relay

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"
)

const maxJWTLifetimeSeconds int64 = 30 * 60

type Claims struct {
	Issuer    string `json:"iss"`
	Audience  string `json:"aud"`
	Subject   string `json:"sub"`
	ParentUID string `json:"parent_uid,omitempty"`
	Expires   int64  `json:"exp"`
	IssuedAt  int64  `json:"iat"`
	NotBefore int64  `json:"nbf"`
	TokenID   string `json:"jti"`
	Product   string `json:"product"`
}

func validateJWT(token string, cfg Config) (Claims, error) {
	var claims Claims
	parts := strings.Split(token, ".")
	if len(parts) != 3 || len(cfg.JWTSecret) < 32 {
		return claims, errors.New("invalid token configuration")
	}
	headerBytes, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return claims, err
	}
	var header map[string]string
	if json.Unmarshal(headerBytes, &header) != nil || header["alg"] != "HS256" || header["typ"] != "JWT" {
		return claims, errors.New("unsupported token")
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return claims, err
	}
	mac := hmac.New(sha256.New, []byte(cfg.JWTSecret))
	_, _ = mac.Write([]byte(parts[0] + "." + parts[1]))
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return claims, errors.New("invalid signature")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return claims, err
	}
	if err = json.Unmarshal(body, &claims); err != nil {
		return claims, err
	}
	if claims.ParentUID != "" {
		id, parseErr := strconv.ParseUint(claims.ParentUID, 10, 64)
		if parseErr != nil || id == 0 || strconv.FormatUint(id, 10) != claims.ParentUID {
			return Claims{}, errors.New("invalid parent identity")
		}
	}
	now := time.Now().Unix()
	if claims.Subject == "" || claims.TokenID == "" || claims.Product != "17" || claims.Issuer != cfg.JWTIssuer || claims.Audience != cfg.JWTAudience || claims.Expires <= now || claims.NotBefore > now || claims.IssuedAt <= 0 || claims.IssuedAt > now+30 || claims.Expires <= claims.IssuedAt || claims.Expires-claims.IssuedAt > maxJWTLifetimeSeconds {
		return Claims{}, errors.New("expired or invalid claims")
	}
	return claims, nil
}
