package server

import (
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"go.uber.org/zap"
)

// TokenResponse is the response for token generation
type TokenResponse struct {
	Token     string    `json:"token"`
	ExpiresAt time.Time `json:"expires_at"`
}

// TokenValidationResponse is the response for token validation
type TokenValidationResponse struct {
	TokenName string    `json:"token_name"`
	ExpiresAt time.Time `json:"expires_at"`
	IssuedAt  time.Time `json:"issued_at"`
}

// jwtCustomClaims defines custom JWT claims
type jwtCustomClaims struct {
	Name string `json:"name"`
	jwt.RegisteredClaims
}

// handleTokenGenerate godoc
// @Summary      Generate JWT token
// @Description  Creates a JWT token valid for 5 minutes
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
		s.logger.Ctx(r.Context()).Error("reading request body failed", zap.Error(err))
		http.Error(w, "invalid request body", http.StatusBadRequest)
		return
	}
	defer r.Body.Close()

	user := "anonymous"
	if len(body) > 0 {
		user = strings.TrimSpace(string(body))
	}

	now := time.Now()
	expiresAt := now.Add(time.Minute * 5)

	claims := &jwtCustomClaims{
		Name: user,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    "backend",
			Subject:   user,
			ExpiresAt: jwt.NewNumericDate(expiresAt),
			IssuedAt:  jwt.NewNumericDate(now),
			NotBefore: jwt.NewNumericDate(now),
		},
	}

	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	t, err := token.SignedString([]byte(s.cfg.JWTSecret))
	if err != nil {
		s.logger.Ctx(r.Context()).Error("signing token failed", zap.Error(err))
		http.Error(w, "failed to generate token", http.StatusInternalServerError)
		return
	}

	s.logger.Ctx(r.Context()).Info("token generated", zap.String("user", user))

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
	authHeader := r.Header.Get("Authorization")
	if authHeader == "" {
		http.Error(w, "authorization header required", http.StatusUnauthorized)
		return
	}

	parts := strings.SplitN(authHeader, " ", 2)
	if len(parts) != 2 || !strings.EqualFold(parts[0], "bearer") {
		http.Error(w, "authorization bearer header required", http.StatusUnauthorized)
		return
	}

	tokenString := parts[1]
	claims := &jwtCustomClaims{}

	token, err := jwt.ParseWithClaims(tokenString, claims, func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodHMAC); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		return []byte(s.cfg.JWTSecret), nil
	})

	if err != nil {
		s.logger.Ctx(r.Context()).Warn("token validation failed", zap.Error(err))
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	if !token.Valid {
		http.Error(w, "invalid token", http.StatusUnauthorized)
		return
	}

	if claims.Issuer != "backend" {
		http.Error(w, "invalid issuer", http.StatusUnauthorized)
		return
	}

	s.logger.Ctx(r.Context()).Info("token validated", zap.String("user", claims.Name))

	respondJSON(w, http.StatusOK, TokenValidationResponse{
		TokenName: claims.Name,
		ExpiresAt: claims.ExpiresAt.Time,
		IssuedAt:  claims.IssuedAt.Time,
	})
}
