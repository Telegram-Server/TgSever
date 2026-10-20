package domain

import (
	"errors"
	"time"
	"unicode/utf8"
)

var (
	ErrInviteExpireDateInvalid = errors.New("invite expire date invalid")
	ErrInviteUsageLimitInvalid = errors.New("invite usage limit invalid")
	ErrInviteTitleInvalid      = errors.New("invite title invalid")
)

// ValidateChannelInviteParameters accepts zero as an omitted/cleared limit.
// TDLib createChatInviteLink requires member_limit=0 for join-request links:
// https://github.com/tdlib/td/blob/master/td/generate/scheme/td_api.tl
func ValidateChannelInviteParameters(title string, expireDate, usageLimit int, requestNeeded bool, date int) error {
	if date == 0 {
		date = int(time.Now().Unix())
	}
	if expireDate < 0 || (expireDate != 0 && expireDate <= date) {
		return ErrInviteExpireDateInvalid
	}
	if usageLimit < 0 || usageLimit > 99999 || (requestNeeded && usageLimit != 0) {
		return ErrInviteUsageLimitInvalid
	}
	if !utf8.ValidString(title) || utf8.RuneCountInString(title) > MaxChannelInviteTitleLength {
		return ErrInviteTitleInvalid
	}
	return nil
}
