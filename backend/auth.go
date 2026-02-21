package main

import (
	"crypto/subtle"
	"net/http"
	"os"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

const cookieName = "artshare_admin"

func adminUser() string { return os.Getenv("ADMIN_USERNAME") }
func adminPass() string { return os.Getenv("ADMIN_PASSWORD") }
func jwtSecret() []byte { return []byte(os.Getenv("JWT_SECRET")) }

func setAdminCookie(w http.ResponseWriter) error {
	claims := jwt.MapClaims{
		"admin": true,
		"exp":   time.Now().Add(7 * 24 * time.Hour).Unix(),
	}
	tok := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	signed, err := tok.SignedString(jwtSecret())
	if err != nil {
		return err
	}
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    signed,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   false, // set true after you add HTTPS custom domain/proxy
		MaxAge:   int((7 * 24 * time.Hour).Seconds()),
	})
	return nil
}

func clearAdminCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     cookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		MaxAge:   -1,
	})
}

func isAdmin(r *http.Request) bool {
	c, err := r.Cookie(cookieName)
	if err != nil || c.Value == "" {
		return false
	}
	tok, err := jwt.Parse(c.Value, func(t *jwt.Token) (any, error) {
		return jwtSecret(), nil
	})
	return err == nil && tok.Valid
}

func requireAdmin(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isAdmin(r) {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

// constant time compare
func safeEq(a, b string) bool {
	if len(a) != len(b) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}
