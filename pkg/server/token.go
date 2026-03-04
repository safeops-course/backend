package server

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

const timeRFC3339 = time.RFC3339

// TokenResponse is the response for token generation.
type TokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// TokenValidationResponse is the response for token validation.
type TokenValidationResponse struct {
	TokenName string    `json:"token_name"`
	ExpiresAt time.Time `json:"expires_at"`
	IssuedAt  time.Time `json:"issued_at"`
}

// jwtCustomClaims defines custom JWT claims.
type jwtCustomClaims struct {
	Name string `json:"name"`
	jwt.RegisteredClaims
}

func (s *Server) tokenTTL() time.Duration {
	if s.cfg.JWTTokenTTLMinutes <= 0 {
		return time.Hour
	}
	return time.Duration(s.cfg.JWTTokenTTLMinutes) * time.Minute
}

func (s *Server) issueToken(user string) (string, time.Time, error) {
	normalizedUser := strings.TrimSpace(user)
	if normalizedUser == "" {
		normalizedUser = "anonymous"
	}

	now := time.Now().UTC()
	expiresAt := now.Add(s.tokenTTL())

	claims := &jwtCustomClaims{
		Name: normalizedUser,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "backend",
			Subject:   normalizedUser,
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := token.SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		return "", time.Time{}, err
	}

	return signed, expiresAt, nil
}

func (s *Server) validateToken(tokenString string) (*jwtCustomClaims, error) {
	claims := &jwtCustomClaims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(s.cfg.JWTSecret), nil
	})
	if err != nil {
		return nil, err
	}
	if !token.Valid {
		return nil, errors.New("invalid token")
	}
	if claims.Issuer != "backend" {
		return nil, errors.New("invalid issuer")
	}
	if strings.TrimSpace(claims.Name) == "" {
		return nil, errors.New("invalid subject")
	}

	return claims, nil
}

func parseBearerToken(r *http.Request) (string, error) {
	authHeader := strings.TrimSpace(r.Header.Get("Authorization"))
	if authHeader == "" {
		return "", errors.New("authorization header required")
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		return "", errors.New("authorization bearer header required")
	}

	tokenString := strings.TrimSpace(parts[1])
	if tokenString == "" {
		return "", errors.New("authorization token required")
	}
	return tokenString, nil
}

// handleTokenGenerate godoc
// @Summary      Generate JWT token
// @Description  Creates a JWT token valid for configured TTL
// @Tags         Auth
// @Accept       plain
// @Produce      json
// @Param        body  body  string  false  "Username (defaults to 'anonymous')"
// @Success      200  {object}  TokenResponse
// @Failure      400  {string}  string  "Bad request"
// @Failure      500  {string}  string  "Internal error"
// @Router       /token [post]
func (s *Server) handleTokenGenerate(w http.ResponseWriter, r *http.Request) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		s.logger.Ctx(r.Context()).Error("reading request body failed", appendTraceFields(r.Context(), zap.Error(err))...)
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	user := strings.TrimSpace(string(body))
	if user == "" {
		user = "anonymous"
	}

	t, expiresAt, err := s.issueToken(user)
	if err != nil {
		s.logger.Ctx(r.Context()).Error("signing token failed", appendTraceFields(r.Context(), zap.Error(err))...)
		http.Error(w, "failed to generate token", http.StatusInternalServerError)
		return
	}

	s.logger.Ctx(r.Context()).Info("token generated", appendTraceFields(r.Context(), zap.String("user", user))...)

	respondJSON(w, http.StatusOK, TokenResponse{
		Token:     t,
		ExpiresAt: expiresAt,
	})
}

// handleTokenValidate godoc
// @Summary      Validate JWT token
// @Description  Validates a JWT token from the Authorization header
// @Tags         Auth
// @Produce      json
// @Security     BearerAuth
// @Success      200  {object}  TokenValidationResponse
// @Failure      401  {string}  string  "Unauthorized"
// @Router       /token/validate [get]
func (s *Server) handleTokenValidate(w http.ResponseWriter, r *http.Request) {
	tokenString, err := parseBearerToken(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	claims, err := s.validateToken(tokenString)
	if err != nil {
		s.logger.Ctx(r.Context()).Warn("token validation failed", appendTraceFields(r.Context(), zap.Error(err))...)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	s.logger.Ctx(r.Context()).Info("token validated", appendTraceFields(r.Context(), zap.String("user", claims.Name))...)

	respondJSON(w, http.StatusOK, TokenValidationResponse{
		TokenName: claims.Name,
		ExpiresAt: claims.ExpiresAt.Time,
		IssuedAt:  claims.IssuedAt.Time,
	})
}
