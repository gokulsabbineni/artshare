package main

import (
	"context"
	"encoding/json"
	"log"
	"mime/multipart"
	"net/http"
	"os"
	"regexp"
	"strings"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type post struct {
	ID        int64     `json:"id"`
	Title     string    `json:"title"`
	Thoughts  string    `json:"thoughts"`
	ImageURL  string    `json:"image_url"`
	Slug      string    `json:"slug"`
	CreatedAt time.Time `json:"created_at"`
}

var slugClean = regexp.MustCompile(`[^a-z0-9-]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, " ", "-")
	s = slugClean.ReplaceAllString(s, "")
	s = strings.Trim(s, "-")
	if s == "" {
		return "post"
	}
	return s
}

func main() {
	addr := env("ADDR", ":8080")
	dbURL := mustEnv("DATABASE_URL")

	// required env
	_ = mustEnv("ADMIN_USERNAME")
	_ = mustEnv("ADMIN_PASSWORD")
	_ = mustEnv("JWT_SECRET")
	_ = mustEnv("CLOUDINARY_CLOUD_NAME")
	_ = mustEnv("CLOUDINARY_API_KEY")
	_ = mustEnv("CLOUDINARY_API_SECRET")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	db, err := openDB(ctx, dbURL)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()

	if err := migrate(context.Background(), db); err != nil {
		log.Fatal(err)
	}

	mux := http.NewServeMux()

	// CORS for your Pages frontend
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) })
	mux.HandleFunc("/login", withCORS(loginHandler))
	mux.HandleFunc("/logout", withCORS(logoutHandler))
	mux.HandleFunc("/admin/posts", withCORS(requireAdmin(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			adminListPosts(db)(w, r)
			return
		}
		if r.Method == "POST" {
			adminCreatePost(db)(w, r)
			return
		}
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})))
	mux.HandleFunc("/posts/", withCORS(publicGetPost(db))) // GET /posts/{slug}

	server := &http.Server{
		Addr:         addr,
		Handler:      mux,
		ReadTimeout:  20 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	log.Printf("listening on %s", addr)
	log.Fatal(server.ListenAndServe())
}

func env(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}
func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatalf("missing env var %s", k)
	}
	return v
}

func withCORS(next http.HandlerFunc) http.HandlerFunc {
	origin := os.Getenv("FRONTEND_ORIGIN") // e.g. https://yourpages.pages.dev
	return func(w http.ResponseWriter, r *http.Request) {
		if origin != "" {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
			w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		}
		if r.Method == "OPTIONS" {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next(w, r)
	}
}

func loginHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var in struct {
		Username string `json:"username"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
		http.Error(w, "bad json", http.StatusBadRequest)
		return
	}

	if safeEq(in.Username, adminUser()) && safeEq(in.Password, adminPass()) {
		if err := setAdminCookie(w); err != nil {
			http.Error(w, "login error", http.StatusInternalServerError)
			return
		}
		json.NewEncoder(w).Encode(map[string]any{"ok": true})
		return
	}
	http.Error(w, "invalid credentials", http.StatusUnauthorized)
}

func logoutHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	clearAdminCookie(w)
	json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

func adminListPosts(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		rows, err := db.Query(r.Context(), `
			SELECT id, title, thoughts, image_url, slug, created_at
			FROM posts ORDER BY created_at DESC LIMIT 200
		`)
		if err != nil {
			http.Error(w, "db error", 500)
			return
		}
		defer rows.Close()

		var out []post
		for rows.Next() {
			var p post
			if err := rows.Scan(&p.ID, &p.Title, &p.Thoughts, &p.ImageURL, &p.Slug, &p.CreatedAt); err != nil {
				http.Error(w, "db error", 500)
				return
			}
			out = append(out, p)
		}
		json.NewEncoder(w).Encode(out)
	}
}

func adminCreatePost(db *pgxpool.Pool) http.HandlerFunc {
	const maxUploadMB = 10
	maxBytes := int64(maxUploadMB) * 1024 * 1024

	return func(w http.ResponseWriter, r *http.Request) {
		// multipart upload from admin.html
		r.Body = http.MaxBytesReader(w, r.Body, maxBytes)
		if err := r.ParseMultipartForm(maxBytes); err != nil {
			http.Error(w, "upload too large or invalid form", http.StatusBadRequest)
			return
		}
		title := strings.TrimSpace(r.FormValue("title"))
		thoughts := strings.TrimSpace(r.FormValue("thoughts"))
		if title == "" || thoughts == "" {
			http.Error(w, "title and thoughts required", 400)
			return
		}
		file, fh, err := r.FormFile("image")
		if err != nil {
			http.Error(w, "image required", 400)
			return
		}
		defer file.Close()

		imageURL, err := uploadToCloudinary(file, sanitizeFilename(fh))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}

		base := slugify(title)
		slug := base
		for i := 0; i < 5; i++ {
			var exists bool
			if err := db.QueryRow(r.Context(), `SELECT EXISTS(SELECT 1 FROM posts WHERE slug=$1)`, slug).Scan(&exists); err != nil {
				http.Error(w, "db error", 500)
				return
			}
			if !exists {
				break
			}
			slug = base + "-" + time.Now().Format("20060102150405")
		}

		_, err = db.Exec(r.Context(), `
			INSERT INTO posts(title, thoughts, image_url, slug)
			VALUES($1,$2,$3,$4)
		`, title, thoughts, imageURL, slug)
		if err != nil {
			http.Error(w, "db error", 500)
			return
		}

		json.NewEncoder(w).Encode(map[string]any{
			"ok":   true,
			"slug": slug,
			"url":  "/posts/" + slug,
		})
	}
}

func sanitizeFilename(fh *multipart.FileHeader) string {
	// safe-ish filename, Cloudinary doesn’t rely on it, but good to keep simple
	name := strings.ToLower(fh.Filename)
	name = strings.ReplaceAll(name, " ", "-")
	name = slugClean.ReplaceAllString(name, "")
	if name == "" {
		name = "upload"
	}
	return name
}

func publicGetPost(db *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			http.Error(w, "method not allowed", 405)
			return
		}
		slug := strings.TrimPrefix(r.URL.Path, "/posts/")
		if slug == "" {
			http.NotFound(w, r)
			return
		}

		var p post
		err := db.QueryRow(r.Context(), `
			SELECT id, title, thoughts, image_url, slug, created_at
			FROM posts WHERE slug=$1
		`, slug).Scan(&p.ID, &p.Title, &p.Thoughts, &p.ImageURL, &p.Slug, &p.CreatedAt)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		json.NewEncoder(w).Encode(p)
	}
}
