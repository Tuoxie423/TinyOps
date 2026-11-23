package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"io"
	"net"
	"net/http"
	"sync"
	"time"
)

const cookieName = "pm_session"

type failure struct {
	count int
	last  time.Time
}

type Auth struct {
	password string
	ttl      time.Duration

	mu       sync.Mutex
	sessions map[string]time.Time
	fails    map[string]*failure
}

func New(password string, ttl time.Duration) *Auth {
	if ttl <= 0 {
		ttl = 24 * time.Hour
	}
	return &Auth{
		password: password,
		ttl:      ttl,
		sessions: make(map[string]time.Time),
		fails:    make(map[string]*failure),
	}
}

func (a *Auth) CheckPassword(p string) bool {
	if a.password == "" {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(p), []byte(a.password)) == 1
}

func (a *Auth) NewSession(w http.ResponseWriter) error {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return err
	}
	token := hex.EncodeToString(b)

	a.mu.Lock()
	a.sessions[token] = time.Now().Add(a.ttl)
	now := time.Now()
	for k, exp := range a.sessions {
		if now.After(exp) {
			delete(a.sessions, k)
		}
	}
	a.mu.Unlock()

	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(a.ttl.Seconds()),
	})
	return nil
}

func (a *Auth) Logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(cookieName); err == nil {
		a.mu.Lock()
		delete(a.sessions, c.Value)
		a.mu.Unlock()
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

func (a *Auth) valid(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false
	}
	a.mu.Lock()
	exp, ok := a.sessions[c.Value]
	if ok && time.Now().After(exp) {
		delete(a.sessions, c.Value)
		ok = false
	}
	a.mu.Unlock()
	return ok
}

func (a *Auth) Require(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.valid(r) {
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":"未登录或会话已过期"}`)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (a *Auth) Throttled(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, ok := a.fails[ip]
	if !ok {
		return false
	}
	if time.Since(f.last) > time.Minute {
		delete(a.fails, ip)
		return false
	}
	return f.count >= 10
}

func (a *Auth) RecordFailure(ip string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	f, ok := a.fails[ip]
	if !ok || time.Since(f.last) > time.Minute {
		a.fails[ip] = &failure{count: 1, last: time.Now()}
		return
	}
	f.count++
	f.last = time.Now()
}

func (a *Auth) ClearFailures(ip string) {
	a.mu.Lock()
	delete(a.fails, ip)
	a.mu.Unlock()
}

func ClientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
