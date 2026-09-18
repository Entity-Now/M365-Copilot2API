package web

import (
	"bytes"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestGeneratedImageCacheByteBudget(t *testing.T) {
	s := &Server{generatedImages: map[string]generatedImage{}}
	chunk := make([]byte, maxGeneratedImageCacheBytes/2+1)
	first := s.storeGeneratedImage(chunk, "image/png")
	time.Sleep(time.Millisecond)
	second := s.storeGeneratedImage(chunk, "image/png")

	if _, ok := s.generatedImages[first]; ok {
		t.Fatal("oldest image was not evicted when the byte budget was exceeded")
	}
	if _, ok := s.generatedImages[second]; !ok {
		t.Fatal("newest image is missing from the cache")
	}
}

func TestImageAPIDisabledByDefault(t *testing.T) {
	s := &Server{settings: &settingsStore{v: defaultRuntimeSettings()}}
	w := httptest.NewRecorder()
	r := httptest.NewRequest(http.MethodPost, "/v1/images/generations", bytes.NewBufferString(`{"prompt":"test"}`))
	s.imageGenerations(w, r)
	if w.Code != http.StatusNotImplemented {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
}

func TestImageEditsValidation(t *testing.T) {
	t.Run("method", func(t *testing.T) {
		w := httptest.NewRecorder()
		(&Server{}).imageEdits(w, httptest.NewRequest(http.MethodGet, "/v1/images/edits", nil))
		if w.Code != http.StatusMethodNotAllowed {
			t.Fatalf("status=%d want %d", w.Code, http.StatusMethodNotAllowed)
		}
	})

	t.Run("prompt", func(t *testing.T) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		part, err := writer.CreateFormFile("image", "image.png")
		if err != nil {
			t.Fatal(err)
		}
		_, _ = part.Write([]byte("not reached without a prompt"))
		_ = writer.Close()
		r := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		w := httptest.NewRecorder()
		(&Server{settings: &settingsStore{v: runtimeSettings{EnableImageAPI: true}}}).imageEdits(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want %d", w.Code, http.StatusBadRequest)
		}
	})

	t.Run("image", func(t *testing.T) {
		var body bytes.Buffer
		writer := multipart.NewWriter(&body)
		_ = writer.WriteField("prompt", "make it blue")
		_ = writer.Close()
		r := httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
		r.Header.Set("Content-Type", writer.FormDataContentType())
		w := httptest.NewRecorder()
		(&Server{settings: &settingsStore{v: runtimeSettings{EnableImageAPI: true}}}).imageEdits(w, r)
		if w.Code != http.StatusBadRequest {
			t.Fatalf("status=%d want %d", w.Code, http.StatusBadRequest)
		}
	})
}
