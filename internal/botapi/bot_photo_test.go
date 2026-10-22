package botapi

import (
	"context"
	"errors"
	"net/http"
	"testing"
)

// fakeBotProfilePhotoGateway 在默认假网关上补 BotProfilePhotoGateway，记录调用的机器人。
type fakeBotProfilePhotoGateway struct {
	*fakeBotAPIGateway
	removeCalls []int64
	err         error
}

func (g *fakeBotProfilePhotoGateway) BotAPIRemoveMyProfilePhoto(_ context.Context, botID int64) (bool, error) {
	g.removeCalls = append(g.removeCalls, botID)
	if g.err != nil {
		return false, g.err
	}
	return true, nil
}

func TestRemoveMyProfilePhotoReturnsTrue(t *testing.T) {
	gateway := &fakeBotProfilePhotoGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}

	resp := decodeAPIResponse(t, callMethod(t, newTestHandler(t, gateway), "removeMyProfilePhoto", `{}`))

	if !resp.OK || string(resp.Result) != "true" {
		t.Fatalf("removeMyProfilePhoto = %+v, want ok true", resp)
	}
	if len(gateway.removeCalls) != 1 || gateway.removeCalls[0] != testBotProfile.BotUserID {
		t.Fatalf("gateway calls = %v, want one call for bot %d", gateway.removeCalls, testBotProfile.BotUserID)
	}
}

func TestRemoveMyProfilePhotoReportsGatewayError(t *testing.T) {
	gateway := &fakeBotProfilePhotoGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}, err: errors.New("BOT_INVALID")}

	rec := callMethod(t, newTestHandler(t, gateway), "removeMyProfilePhoto", `{}`)
	resp := decodeAPIResponse(t, rec)

	if resp.OK || rec.Code != http.StatusBadRequest || resp.Description != "BOT_INVALID" {
		t.Fatalf("removeMyProfilePhoto with gateway error = %d %+v, want 400 BOT_INVALID", rec.Code, resp)
	}
}

// 网关没有实现这组接口时回 501，与其他可选方法一致。
func TestRemoveMyProfilePhotoWithoutGatewaySupport(t *testing.T) {
	rec := callMethod(t, newTestHandler(t, &fakeBotAPIGateway{}), "removeMyProfilePhoto", `{}`)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("removeMyProfilePhoto without gateway = %d %s, want 501", rec.Code, rec.Body.String())
	}
}
