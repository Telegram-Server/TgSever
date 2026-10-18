package adminapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"telesrv/internal/admin"
)

type messageMediaService struct {
	fakeService
	thumb string
	full  bool
}

func (s *messageMediaService) MessagePhoto(_ context.Context, id int64, full bool) ([]byte, string, bool, error) {
	s.full = full
	if id != 7 {
		return nil, "", false, nil
	}
	return []byte("png-bytes"), "image/png", true, nil
}

func (s *messageMediaService) MessageDocument(_ context.Context, id int64, thumb string) (admin.MessageMediaFile, bool, error) {
	s.thumb = thumb
	if id != 21 {
		return admin.MessageMediaFile{}, false, nil
	}
	if thumb != "" {
		return admin.MessageMediaFile{Bytes: []byte("thumb"), MimeType: "image/jpeg"}, true, nil
	}
	return admin.MessageMediaFile{Bytes: []byte("<html>"), MimeType: "text/html", Attachment: true}, true, nil
}

func getMedia(t *testing.T, svc Service, path string) *httptest.ResponseRecorder {
	t.Helper()
	srv := &Server{token: "secret", svc: svc}
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.Header.Set("Authorization", "Bearer secret")
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	return rec
}

func TestMessagePhotoRouteServesTheChosenSize(t *testing.T) {
	svc := &messageMediaService{}
	rec := getMedia(t, svc, "/v1/media/photos/7?full=1")
	if rec.Code != http.StatusOK || rec.Header().Get("Content-Type") != "image/png" || rec.Body.String() != "png-bytes" || !svc.full {
		t.Fatalf("status=%d type=%q body=%q full=%v", rec.Code, rec.Header().Get("Content-Type"), rec.Body.String(), svc.full)
	}
	if rec := getMedia(t, svc, "/v1/media/photos/8"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing photo status=%d, want 404", rec.Code)
	}
}

// A stored file is user-supplied bytes. Served inline, an uploaded HTML file
// would run in the panel's origin, so a file is always a sniff-proof download.
func TestMessageDocumentRouteNeverRendersAFileInline(t *testing.T) {
	svc := &messageMediaService{}
	rec := getMedia(t, svc, "/v1/media/documents/21")
	if rec.Code != http.StatusOK || rec.Body.String() != "<html>" {
		t.Fatalf("status=%d body=%q", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("Content-Disposition"); got != `attachment; filename="document-21"` {
		t.Fatalf("Content-Disposition=%q, want an attachment", got)
	}
	if got := rec.Header().Get("X-Content-Type-Options"); got != "nosniff" {
		t.Fatalf("X-Content-Type-Options=%q, want nosniff", got)
	}

	rec = getMedia(t, svc, "/v1/media/documents/21?thumb=m")
	if rec.Code != http.StatusOK || svc.thumb != "m" || rec.Header().Get("Content-Disposition") != "" || rec.Header().Get("Content-Type") != "image/jpeg" {
		t.Fatalf("thumb status=%d thumb=%q disposition=%q type=%q", rec.Code, svc.thumb, rec.Header().Get("Content-Disposition"), rec.Header().Get("Content-Type"))
	}
	if rec := getMedia(t, svc, "/v1/media/documents/22"); rec.Code != http.StatusNotFound {
		t.Fatalf("missing document status=%d, want 404", rec.Code)
	}
}
