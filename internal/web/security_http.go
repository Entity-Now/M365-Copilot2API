package web

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"net/http"
	"os"
	"strconv"
	"strings"
)

//go:embed all:web
var webFS embed.FS

var webContent http.FileSystem

// supportedLocales mirrors SUPPORTED_LOCALES in the console pages.
var supportedLocales = map[string]bool{
	"en": true, "zh-CN": true, "zh-TW": true, "ja": true, "ko": true,
	"es": true, "fr": true, "de": true, "pt-BR": true, "ru": true, "ar": true,
}

// defaultLocaleSnippet returns an inline <script> that overrides the console
// default language (window.M365_DEFAULT_LOCALE) based on M365_DEFAULT_LOCALE.
// Values are validated against a whitelist so a malformed env var can never
// inject markup. Empty when unset, invalid, or equal to the built-in zh-CN.
func defaultLocaleSnippet() string {
	v := strings.TrimSpace(os.Getenv("M365_DEFAULT_LOCALE"))
	if v == "" || v == "zh-CN" || !supportedLocales[v] {
		return ""
	}
	return "<script>window.M365_DEFAULT_LOCALE=" + strconv.Quote(v) + ";</script>"
}

// pageWithDefaultLocale injects the locale snippet right after <head>.
func pageWithDefaultLocale(page []byte) []byte {
	snippet := defaultLocaleSnippet()
	if snippet == "" {
		return page
	}
	i := bytes.Index(page, []byte("<head>"))
	if i < 0 {
		return page
	}
	i += len("<head>")
	out := make([]byte, 0, len(page)+len(snippet))
	out = append(out, page[:i]...)
	out = append(out, snippet...)
	out = append(out, page[i:]...)
	return out
}

func init() {
	sub, err := fs.Sub(webFS, "web")
	if err != nil {
		panic(err)
	}
	webContent = http.FS(sub)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("X-Frame-Options", "DENY")
		w.Header().Set("Referrer-Policy", "no-referrer")
		w.Header().Set("Permissions-Policy", "camera=(), microphone=(), geolocation=(), payment=(), usb=()")
		w.Header().Set("Content-Security-Policy", "default-src 'self'; base-uri 'none'; frame-ancestors 'none'; object-src 'none'; form-action 'self'; connect-src 'self'; img-src 'self' data:; style-src 'self' 'unsafe-inline' https://fonts.googleapis.com; font-src 'self' https://fonts.gstatic.com; script-src 'self' 'unsafe-inline' https://unpkg.com https://cdn.jsdelivr.net")
		if r.URL.Path == "/" || r.URL.Path == "/login" || r.URL.Path == "/api/admin/login" || r.URL.Path == "/api/admin/session" || r.URL.Path == "/api/admin/change-password" {
			w.Header().Set("Cache-Control", "no-store")
		}
		next.ServeHTTP(w, r)
	})
}

func (s *Server) rootPage(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" && r.URL.Path != "/login" && r.URL.Path != "/conversation" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeOpenAIError(w, http.StatusMethodNotAllowed, "invalid_request_error", "method not allowed")
		return
	}
	name := "index.html"
	if r.URL.Path == "/login" {
		name = "login.html"
	} else if r.URL.Path == "/conversation" {
		name = "conversation.html"
	}
	f, err := webContent.Open(name)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", "web interface unavailable")
		return
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", "web interface unavailable")
		return
	}
	body, err := io.ReadAll(f)
	if err != nil {
		writeOpenAIError(w, http.StatusInternalServerError, "server_error", "web interface unavailable")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	http.ServeContent(w, r, name, st.ModTime(), bytes.NewReader(pageWithDefaultLocale(body)))
}
