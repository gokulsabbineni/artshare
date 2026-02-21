package main

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"time"
)

type cloudinaryResp struct {
	SecureURL string `json:"secure_url"`
	URL       string `json:"url"`
}

func cloudName() string   { return os.Getenv("CLOUDINARY_CLOUD_NAME") }
func cloudKey() string    { return os.Getenv("CLOUDINARY_API_KEY") }
func cloudSecret() string { return os.Getenv("CLOUDINARY_API_SECRET") }

func signCloudinary(params map[string]string) string {
	// Cloudinary signature: sha1 of "k=v&k=v...<api_secret>"
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	for i, k := range keys {
		if i > 0 {
			buf.WriteString("&")
		}
		buf.WriteString(k)
		buf.WriteString("=")
		buf.WriteString(params[k])
	}
	buf.WriteString(cloudSecret())

	h := sha1.Sum(buf.Bytes())
	return hex.EncodeToString(h[:])
}

func uploadToCloudinary(file multipart.File, filename string) (string, error) {
	// Read file into memory (OK for small uploads; enforce max upload size in handler)
	b, err := io.ReadAll(file)
	if err != nil {
		return "", err
	}

	timestamp := strconv.FormatInt(time.Now().Unix(), 10)
	paramsToSign := map[string]string{
		"timestamp": timestamp,
		"folder":    "artshare",
	}
	signature := signCloudinary(paramsToSign)

	form := url.Values{}
	form.Set("api_key", cloudKey())
	form.Set("timestamp", timestamp)
	form.Set("folder", "artshare")
	form.Set("signature", signature)

	// multipart request
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)

	_ = writer.WriteField("api_key", cloudKey())
	_ = writer.WriteField("timestamp", timestamp)
	_ = writer.WriteField("folder", "artshare")
	_ = writer.WriteField("signature", signature)

	part, err := writer.CreateFormFile("file", filename)
	if err != nil {
		return "", err
	}
	if _, err := part.Write(b); err != nil {
		return "", err
	}
	_ = writer.Close()

	endpoint := fmt.Sprintf("https://api.cloudinary.com/v1_1/%s/image/upload", cloudName())
	req, _ := http.NewRequest("POST", endpoint, &body)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("cloudinary upload failed: %s", string(raw))
	}

	var out cloudinaryResp
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.SecureURL != "" {
		return out.SecureURL, nil
	}
	return out.URL, nil
}
