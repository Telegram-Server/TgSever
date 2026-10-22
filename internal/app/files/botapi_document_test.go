package files

import (
	"context"
	"net/http"
	"net/http/httptest"
	"telesrv/internal/domain"
	"testing"
)

func TestBotAPIURLDocumentKeepsKindOnFileReuse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/mpeg")
		_, _ = w.Write([]byte("audio payload"))
	}))
	defer server.Close()
	blobs, err := NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	service := NewService(newFakeMediaStore(), blobs, 2, WithVideoThumbnailer(nil))
	service.externalMedia = newExternalMediaFetcher(DefaultExternalMediaMaxBytes, DefaultExternalMediaRatePerMin, true)
	ctx := context.Background()
	document, err := service.CreateDocumentFromURLWithSpec(ctx, server.URL+"/track.mp3", domain.DocumentSpec{Attributes: []domain.DocumentAttribute{{Kind: domain.DocAttrAudio, AudioDuration: 9, Title: "A track", Performer: "An artist"}}})
	if err != nil {
		t.Fatal(err)
	}
	reloaded, found, err := service.GetDocument(ctx, document.ID)
	if err != nil || !found {
		t.Fatalf("reload=%+v %v %v", reloaded, found, err)
	}
	audio := false
	for _, attribute := range reloaded.Attributes {
		if attribute.Kind == domain.DocAttrAudio && attribute.AudioDuration == 9 && attribute.Title == "A track" && attribute.Performer == "An artist" {
			audio = true
		}
	}
	if !audio || reloaded.MimeType != "audio/mpeg" {
		t.Fatalf("persisted kind lost: %+v", reloaded)
	}
}
