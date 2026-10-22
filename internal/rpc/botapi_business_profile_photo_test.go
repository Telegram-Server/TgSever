package rpc

import (
	"bytes"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/iamxvbaba/td/tg"
	appfiles "telesrv/internal/app/files"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestBotAPIBusinessProfilePhotoHTTPLifecycleAndAuthorization(t *testing.T) {
	for _, mode := range []string{"authorized", "missing_right", "revoked"} {
		t.Run(mode, func(t *testing.T) {
			f, account, connection := newBusinessFixture(t, domain.BusinessBotRights{EditProfilePhoto: true})
			blobs, err := appfiles.NewLocalFS(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			media := memory.NewMediaStore()
			files := appfiles.NewService(media, blobs, 2)
			f.router.deps.Files = files
			f.users = appusers.NewService(f.userStore, appusers.WithPhotoProvider(media))
			f.router.deps.Users = f.users
			server := profileHTTPServer(t, f)
			token := domain.FormatBotToken(f.bot.ID, "secret")
			var photo bytes.Buffer
			if err := png.Encode(&photo, image.NewRGBA(image.Rect(0, 0, 320, 240))); err != nil {
				t.Fatal(err)
			}
			upload := func(method string) (profileHTTPResponse, int) {
				t.Helper()
				var body bytes.Buffer
				form := multipart.NewWriter(&body)
				if method == "setBusinessAccountProfilePhoto" {
					if err := form.WriteField("business_connection_id", connection.ConnectionID); err != nil {
						t.Fatal(err)
					}
				}
				if err := form.WriteField("photo", `{"type":"static","photo":"attach://avatar"}`); err != nil {
					t.Fatal(err)
				}
				part, err := form.CreateFormFile("avatar", "avatar.png")
				if err != nil {
					t.Fatal(err)
				}
				if _, err := part.Write(photo.Bytes()); err != nil {
					t.Fatal(err)
				}
				if err := form.Close(); err != nil {
					t.Fatal(err)
				}
				request := httptest.NewRequest(http.MethodPost, "/bot"+token+"/"+method, bytes.NewReader(body.Bytes()))
				request.Header.Set("Content-Type", form.FormDataContentType())
				response := httptest.NewRecorder()
				server.Handler.ServeHTTP(response, request)
				var out profileHTTPResponse
				if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
					t.Fatalf("%s: %s: %v", method, response.Body, err)
				}
				return out, response.Code
			}
			currentID := func(userID int64) int64 {
				t.Helper()
				photo, found, err := files.CurrentProfilePhotoKind(f.ctx, domain.PeerTypeUser, userID, domain.ProfilePhotoKindProfile)
				if err != nil || !found || photo.ID == 0 {
					t.Fatalf("current photo for %d: %+v found=%v err=%v", userID, photo, found, err)
				}
				return photo.ID
			}
			// A real, nonempty bot avatar makes wrong-principal removal observable.
			if out, status := upload("setMyProfilePhoto"); !out.OK || status != http.StatusOK {
				t.Fatalf("seed bot avatar: %+v status=%d", out, status)
			}
			botPhotoID := currentID(f.bot.ID)
			assertState := func(ownerPhotoIDs ...int64) {
				t.Helper()
				for _, want := range []struct {
					userID int64
					ids    []int64
				}{{f.owner.ID, ownerPhotoIDs}, {f.bot.ID, []int64{botPhotoID}}} {
					photos, total, err := files.GetProfilePhotosKind(f.ctx, domain.PeerTypeUser, want.userID, domain.ProfilePhotoKindProfile, 0, 100, 0)
					if err != nil || total != len(want.ids) || len(photos) != len(want.ids) {
						t.Fatalf("user %d album: %+v total=%d want=%v err=%v", want.userID, photos, total, want.ids, err)
					}
					for i, id := range want.ids {
						if photos[i].ID != id {
							t.Fatalf("user %d album[%d]=%d want=%d", want.userID, i, photos[i].ID, id)
						}
					}
					var wantCurrent int64
					if len(want.ids) != 0 {
						wantCurrent = want.ids[0]
					}
					// Read through the native viewer projection, not just the album rows.
					user, found, err := f.users.ByID(f.ctx, f.member.ID, want.userID)
					if err != nil || !found || user.PhotoID != wantCurrent {
						t.Fatalf("user %d projected avatar=%d want=%d found=%v err=%v", want.userID, user.PhotoID, wantCurrent, found, err)
					}
					current, found, err := files.CurrentProfilePhotoKind(f.ctx, domain.PeerTypeUser, want.userID, domain.ProfilePhotoKindProfile)
					if err != nil || found != (wantCurrent != 0) || current.ID != wantCurrent {
						t.Fatalf("user %d stored avatar=%+v want=%d found=%v err=%v", want.userID, current, wantCurrent, found, err)
					}
				}
			}
			assertState()
			if out, status := upload("setBusinessAccountProfilePhoto"); !out.OK || status != http.StatusOK {
				t.Fatalf("first business avatar: %+v status=%d", out, status)
			}
			firstID := currentID(f.owner.ID)
			assertState(firstID)
			if out, status := upload("setBusinessAccountProfilePhoto"); !out.OK || status != http.StatusOK {
				t.Fatalf("replacement business avatar: %+v status=%d", out, status)
			}
			secondID := currentID(f.owner.ID)
			assertState(secondID, firstID)
			remove := func() (profileHTTPResponse, int) {
				t.Helper()
				return profileHTTPCall(t, server.Handler, token, "removeBusinessAccountProfilePhoto", map[string]any{"business_connection_id": connection.ConnectionID})
			}
			if mode == "authorized" {
				if out, status := remove(); !out.OK || status != http.StatusOK {
					t.Fatalf("remove current business avatar: %+v status=%d", out, status)
				}
				// Ordinary profile removal promotes the preceding active avatar.
				assertState(firstID)
				if out, status := remove(); !out.OK || status != http.StatusOK {
					t.Fatalf("remove last business avatar: %+v status=%d", out, status)
				}
				assertState()
				return
			}
			if mode == "missing_right" {
				connection.Rights = domain.BusinessBotRights{EditName: true}
				if _, err := account.SaveConnectedBusinessBot(f.ctx, f.owner.ID, connection); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.router.onAccountUpdateConnectedBot(WithUserID(f.ctx, f.owner.ID), &tg.AccountUpdateConnectedBotRequest{Deleted: true, Bot: &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}}); err != nil {
					t.Fatal(err)
				}
			}
			if out, status := upload("setBusinessAccountProfilePhoto"); out.OK || status != http.StatusBadRequest {
				t.Fatalf("unauthorized business avatar replacement: %+v status=%d", out, status)
			}
			assertState(secondID, firstID)
			if out, status := remove(); out.OK || status != http.StatusBadRequest {
				t.Fatalf("unauthorized business avatar removal: %+v status=%d", out, status)
			}
			assertState(secondID, firstID)
		})
	}
}
