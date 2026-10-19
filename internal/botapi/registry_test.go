package botapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"testing"

	"telesrv/internal/domain"
)

// 注册表是包级状态：探针只能在 init 里注册一次，否则 -count=2 会因重复注册 panic。
func init() {
	registerMethods(map[string]methodFunc{
		"registryProbe": func(h *handler, w http.ResponseWriter, r *http.Request, botID int64) {
			writeAPIOK(w, map[string]any{"bot_id": botID})
		},
		"registryProbeFails": func(h *handler, w http.ResponseWriter, r *http.Request, botID int64) {
			writeAPIError(w, http.StatusBadRequest, apiErrorDescription(errors.New("rpc error code 400: REGISTRY_PROBE_DENIED")))
		},
	})
	registerErrorMarkers("REGISTRY_PROBE_DENIED")
}

func TestRegisteredMethodIsDispatchedCaseInsensitively(t *testing.T) {
	h := newTestHandler(t, &fakeBotAPIGateway{})

	rec := callMethod(t, h, "REGISTRYPROBE", `{}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp struct {
		OK     bool `json:"ok"`
		Result struct {
			BotID int64 `json:"bot_id"`
		} `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.OK || resp.Result.BotID != testBotProfile.BotUserID {
		t.Fatalf("response = %s, want ok with the authenticated bot id", rec.Body.String())
	}
}

func TestUnregisteredMethodStillAnswersMethodNotFound(t *testing.T) {
	h := newTestHandler(t, &fakeBotAPIGateway{})

	rec := callMethod(t, h, "noSuchMethod", `{}`)
	if rec.Code != http.StatusNotFound || decodeAPIResponse(t, rec).Description != "METHOD_NOT_FOUND" {
		t.Fatalf("status = %d body = %s, want 404 METHOD_NOT_FOUND", rec.Code, rec.Body.String())
	}
}

func TestRegisteredErrorMarkerSurvivesDescription(t *testing.T) {
	h := newTestHandler(t, &fakeBotAPIGateway{})

	rec := callMethod(t, h, "registryProbeFails", `{}`)
	if got := decodeAPIResponse(t, rec).Description; got != "REGISTRY_PROBE_DENIED" {
		t.Fatalf("description = %q, want the registered marker instead of BAD_REQUEST", got)
	}
}

func TestErrorDescriptionPrefersTheLongestMarker(t *testing.T) {
	// MESSAGE_ID_INVALID is a substring of REPLY_MESSAGE_ID_INVALID; the more
	// specific code must win regardless of list order.
	for raw, want := range map[string]string{
		"rpc error code 400: REPLY_MESSAGE_ID_INVALID": "REPLY_MESSAGE_ID_INVALID",
		"rpc error code 400: MESSAGE_ID_INVALID":       "MESSAGE_ID_INVALID",
		"QUERY_ID_INVALID":                             "QUERY_ID_INVALID",
	} {
		if got := apiErrorDescription(errors.New(raw)); got != want {
			t.Fatalf("apiErrorDescription(%q) = %q, want %q", raw, got, want)
		}
	}
}

func TestRegisteringADuplicateMethodPanics(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registering registryProbe twice did not panic")
		}
	}()
	registerMethods(map[string]methodFunc{"RegistryProbe": nil})
}

func TestRegisteredMessageProjectionExtendsMessages(t *testing.T) {
	msg := domain.Message{ID: 7, Date: 1, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 5}, GroupedID: 4242}
	out := apiMessage(msg, nil)
	if out["registry_probe_grouped_id"] != int64(4242) {
		t.Fatalf("projection = %+v, want the registered extension to run", out)
	}
}

func init() {
	registerMessageProjection(func(msg domain.Message, out map[string]any) {
		if msg.GroupedID != 0 {
			out["registry_probe_grouped_id"] = msg.GroupedID
		}
	})
}
