package domain

import (
	"errors"
	"strings"
)

// 已保存铃声的上限。appConfig 的 ringtone_size_max / ringtone_duration_max /
// ringtone_saved_count_max 下发同一组值，客户端预检与服务端校验一致。
const (
	RingtoneSizeMax       int64 = 300 << 10
	RingtoneDurationMax         = 5 // 秒
	RingtoneSavedCountMax       = 100
)

var (
	ErrRingtoneInvalid         = errors.New("ringtone invalid")
	ErrRingtoneMimeInvalid     = errors.New("ringtone mime invalid")
	ErrRingtoneSizeTooBig      = errors.New("ringtone size too big")
	ErrRingtoneDurationTooLong = errors.New("ringtone duration too long")
)

// ValidateRingtone 校验一个文档能否作为通知铃声：音频（audio/* 或带音频属性，含语音）、
// 不超过大小上限、已知时长不超过时长上限。只带文件名属性的新上传文档时长未知，不校验。
func ValidateRingtone(d Document) error {
	audio := strings.HasPrefix(strings.ToLower(d.MimeType), "audio/")
	duration := -1
	for _, attr := range d.Attributes {
		if attr.Kind == DocAttrAudio {
			audio = true
			duration = attr.AudioDuration
		}
	}
	switch {
	case !audio:
		return ErrRingtoneMimeInvalid
	case d.Size > RingtoneSizeMax:
		return ErrRingtoneSizeTooBig
	case duration > RingtoneDurationMax:
		return ErrRingtoneDurationTooLong
	}
	return nil
}
