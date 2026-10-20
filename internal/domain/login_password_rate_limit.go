package domain

import (
	"errors"
	"fmt"
)

// LoginPasswordFloodWaitError 表示密码登录超过了按目标用户的限额，Seconds 是还要等多久，
// RPC 层把它映射成 FLOOD_WAIT_X。
type LoginPasswordFloodWaitError struct {
	Seconds int
}

func (e LoginPasswordFloodWaitError) Error() string {
	if e.Seconds <= 0 {
		return "login password flood wait"
	}
	return fmt.Sprintf("login password flood wait %d seconds", e.Seconds)
}

// NewLoginPasswordFloodWaitError 返回一个至少等待 1 秒的 LoginPasswordFloodWaitError。
func NewLoginPasswordFloodWaitError(seconds int) error {
	if seconds <= 0 {
		seconds = 1
	}
	return LoginPasswordFloodWaitError{Seconds: seconds}
}

// LoginPasswordFloodWaitSeconds 从 err 中取出密码登录限流的等待秒数（至少 1）。
func LoginPasswordFloodWaitSeconds(err error) (int, bool) {
	var wait LoginPasswordFloodWaitError
	if !errors.As(err, &wait) {
		return 0, false
	}
	if wait.Seconds <= 0 {
		return 1, true
	}
	return wait.Seconds, true
}
