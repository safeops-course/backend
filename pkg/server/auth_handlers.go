package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"go.uber.org/zap"
)

type authContextKey string

const authenticatedUserContextKey authContextKey = "authenticatedUser"

type authCredentialsRequest struct {
	Username string `json:"username"`
	Password string `json:"password"`
}

type authUserResponse struct {
	ID       int64  `json:"id"`
	Username string `json:"username"`
}

type authResponse struct {
	Token     string           `json:"token"`
	ExpiresAt string           `json:"expires_at"`
	User      authUserResponse `json:"user"`
}

func (s *Server) authMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenString, err := parseBearerToken(r)
		if err != nil {
			respondJSON(w, http.StatusUnauthorized, map[string]string{"error": err.Error()})
			return
		}

		claims, err := s.validateToken(tokenString)
		if err != nil {
			s.logger.Ctx(r.Context()).Warn("auth token validation failed", zap.Error(err))
			respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
			return
		}

		ctx := context.WithValue(r.Context(), authenticatedUserContextKey, claims.Name)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func (s *Server) authenticatedUsername(ctx context.Context) (string, bool) {
	v := ctx.Value(authenticatedUserContextKey)
	username, ok := v.(string)
	if !ok || strings.TrimSpace(username) == "" {
		return "", false
	}
	return username, true
}

// maxLimiterKeyLength matches the username column (VARCHAR(64)); longer input cannot be a user,
// and a cap keeps an attacker from filling the limiter with megabyte-long keys.
const maxLimiterKeyLength = 64

func loginLimiterKey(username string) string {
	key := strings.ToLower(strings.TrimSpace(username))
	if len(key) > maxLimiterKeyLength {
		key = key[:maxLimiterKeyLength]
	}
	return key
}

func respondTooManyRequests(w http.ResponseWriter, retryAfter time.Duration) {
	seconds := int(math.Ceil(retryAfter.Seconds()))
	if seconds < 1 {
		seconds = 1
	}
	w.Header().Set("Retry-After", strconv.Itoa(seconds))
	respondJSON(w, http.StatusTooManyRequests, map[string]string{"error": "too many requests, try again later"})
}

func (s *Server) handleAuthRegister(w http.ResponseWriter, r *http.Request) {
	if s.users == nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "auth store not initialized"})
		return
	}
	if !s.cfg.RegistrationEnabled {
		respondJSON(w, http.StatusForbidden, map[string]string{"error": "registration is disabled"})
		return
	}
	if allowed, retryAfter := s.registrationLimiter.allow("register"); !allowed {
		s.logger.Ctx(r.Context()).Warn("registration rate limit reached")
		respondTooManyRequests(w, retryAfter)
		return
	}

	var req authCredentialsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBodyBytes)).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	release, ok := s.acquireBcryptSlot(w)
	if !ok {
		return
	}
	user, err := s.users.createUser(r.Context(), req.Username, req.Password)
	release()
	if err != nil {
		switch {
		case errors.Is(err, errUserExists):
			respondJSON(w, http.StatusConflict, map[string]string{"error": "user already exists"})
		case errors.Is(err, errInvalidInput):
			respondJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		default:
			s.logger.Ctx(r.Context()).Error("auth register failed",
				zap.Error(err),
				zap.String("username", strings.TrimSpace(req.Username)),
				zap.String("request_id", chimiddleware.GetReqID(r.Context())),
			)
			respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "unable to create user"})
		}
		return
	}

	token, expiresAt, err := s.issueToken(user.Username)
	if err != nil {
		s.logger.Ctx(r.Context()).Error("token generation failed", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate token"})
		return
	}

	respondJSON(w, http.StatusCreated, authResponse{
		Token:     token,
		ExpiresAt: expiresAt.Format(timeRFC3339),
		User: authUserResponse{
			ID:       user.ID,
			Username: user.Username,
		},
	})
}

func (s *Server) handleAuthLogin(w http.ResponseWriter, r *http.Request) {
	if s.users == nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "auth store not initialized"})
		return
	}

	var req authCredentialsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBodyBytes)).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	if allowed, retryAfter := s.loginLimiter.allow(loginLimiterKey(req.Username)); !allowed {
		s.logger.Ctx(r.Context()).Warn("login rate limit reached", zap.String("username", loginLimiterKey(req.Username)))
		respondTooManyRequests(w, retryAfter)
		return
	}

	release, ok := s.acquireBcryptSlot(w)
	if !ok {
		return
	}
	user, err := s.users.authenticate(r.Context(), req.Username, req.Password)
	release()
	if err != nil {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid username or password"})
		return
	}

	token, expiresAt, err := s.issueToken(user.Username)
	if err != nil {
		s.logger.Ctx(r.Context()).Error("token generation failed", zap.Error(err))
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "failed to generate token"})
		return
	}

	respondJSON(w, http.StatusOK, authResponse{
		Token:     token,
		ExpiresAt: expiresAt.Format(timeRFC3339),
		User: authUserResponse{
			ID:       user.ID,
			Username: user.Username,
		},
	})
}

func (s *Server) handleAuthMe(w http.ResponseWriter, r *http.Request) {
	username, ok := s.authenticatedUsername(r.Context())
	if !ok {
		respondJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
		return
	}

	respondJSON(w, http.StatusOK, map[string]string{"username": username})
}

// bcrypt costs tens of milliseconds of CPU per check, and the per-username
// limiter does not stop many usernames at once. At most bcryptSlots checks run
// in parallel per pod; beyond that the request gets 429 at once instead of
// queueing and starving the probes of CPU.
const bcryptSlots = 2

func (s *Server) acquireBcryptSlot(w http.ResponseWriter) (release func(), ok bool) {
	select {
	case s.bcryptSem <- struct{}{}:
		return func() { <-s.bcryptSem }, true
	default:
		respondTooManyRequests(w, time.Second)
		return nil, false
	}
}
