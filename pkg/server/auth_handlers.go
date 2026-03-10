package server

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

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

func (s *Server) handleAuthRegister(w http.ResponseWriter, r *http.Request) {
	if s.users == nil {
		respondJSON(w, http.StatusInternalServerError, map[string]string{"error": "auth store not initialized"})
		return
	}

	var req authCredentialsRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, maxRequestBodyBytes)).Decode(&req); err != nil {
		respondJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid JSON body"})
		return
	}

	user, err := s.users.createUser(r.Context(), req.Username, req.Password)
	if err != nil {
		switch {
		case errors.Is(err, errUserExists):
			respondJSON(w, http.StatusConflict, map[string]string{"error": "user already exists"})
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

	user, err := s.users.authenticate(r.Context(), req.Username, req.Password)
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
