package main

import (
	"context"
	"encoding/base64"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

type statusWriter struct {
	http.ResponseWriter
	status int
}

func (s *statusWriter) WriteHeader(code int) {
	if s.status != 0 {
		return
	}
	s.status = code
	s.ResponseWriter.WriteHeader(code)
}

func (s *statusWriter) Write(b []byte) (int, error) {
	if s.status == 0 {
		s.status = http.StatusOK
		s.ResponseWriter.WriteHeader(http.StatusOK)
	}
	return s.ResponseWriter.Write(b)
}

func LoggingMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		sw := &statusWriter{ResponseWriter: w}
		next.ServeHTTP(sw, r)

		slog.Info("HTTP",
			"method", r.Method,
			"path", r.URL.Path,
			"status", sw.status,
			"duration", time.Since(start))
	})
}

type UserAuthKey struct {
	Username   string
	Passphrase string
}

type UserCtxKey struct{}

var users map[string]string = map[string]string{
	"admin": "deadbeef",
}

func UserAuthKeyFromContext(ctx context.Context) (UserAuthKey, bool) {
	userAuthKey, ok := ctx.Value(UserCtxKey{}).(UserAuthKey)
	return userAuthKey, ok
}

func validateCredentials(username, password string) bool {
	p, ok := users[username]
	if ok && password == p {
		return true
	}
	return false
}

func AuthMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		username, sikret, ok := r.BasicAuth()
		if !ok {
			w.Header().Set("WWW-Authenticate", `Basic realm="API"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		epassword, epassphrase, ok := strings.Cut(sikret, ":")
		if !ok {
			http.Error(w, "Bad authentification format", http.StatusUnauthorized)
			return
		}
		password, err := base64.StdEncoding.DecodeString(epassword)
		if err != nil {
			http.Error(w, "Bad password format", http.StatusUnauthorized)
			return
		}
		passphrase, err := base64.StdEncoding.DecodeString(epassphrase)
		if err != nil {
			http.Error(w, "Bad passphrase format", http.StatusUnauthorized)
			return
		}

		if !validateCredentials(username, string(password)) {
			w.Header().Set("WWW-Authenticate", `Basic realm="API"`)
			http.Error(w, "Unauthorized", http.StatusUnauthorized)
			return
		}

		ctx := context.WithValue(r.Context(), UserCtxKey{}, UserAuthKey{
			Username:   username,
			Passphrase: string(passphrase),
		})

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}
