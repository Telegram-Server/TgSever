package domain

// NotifyScopeKind 区分通知设置的作用域：具体 peer / 三类全局默认（私聊/群/频道）。
type NotifyScopeKind string

const (
	NotifyScopePeer       NotifyScopeKind = "peer"
	NotifyScopeUsers      NotifyScopeKind = "users"
	NotifyScopeChats      NotifyScopeKind = "chats"
	NotifyScopeBroadcasts NotifyScopeKind = "broadcasts"
)

// NotifyScope 唯一标识一条通知设置：peer 作用域用 Peer(+TopicID 区分 forum 话题，
// 0=整 peer)，三类默认作用域 Peer/TopicID 为空。
type NotifyScope struct {
	Kind    NotifyScopeKind
	Peer    Peer
	TopicID int
}

// NotificationSoundKind 对应 NotificationSound 的四个构造器。
type NotificationSoundKind string

const (
	NotificationSoundDefault  NotificationSoundKind = "default"
	NotificationSoundNone     NotificationSoundKind = "none"
	NotificationSoundLocal    NotificationSoundKind = "local"
	NotificationSoundRingtone NotificationSoundKind = "ringtone"
)

// NotificationSound 是一个声音选择：local 带客户端本地的 Title/Data（系统音名或
// 文件路径，只对写入它的平台有意义），ringtone 带已保存铃声的文档 id。
type NotificationSound struct {
	Kind       NotificationSoundKind `json:"kind"`
	Title      string                `json:"title,omitempty"`
	Data       string                `json:"data,omitempty"`
	RingtoneID int64                 `json:"id,omitempty"`
}

// NotifySoundSlot 是 peerNotifySettings 里按平台分开的声音槽。入参
// inputPeerNotifySettings 只有一个 sound，服务端按发请求会话的平台写进对应槽。
type NotifySoundSlot string

const (
	NotifySoundSlotIOS     NotifySoundSlot = "ios"
	NotifySoundSlotAndroid NotifySoundSlot = "android"
	NotifySoundSlotOther   NotifySoundSlot = "other"
)

// NotifySounds 是三个平台槽，nil=该平台未设置（客户端继承默认）。
type NotifySounds struct {
	IOS     *NotificationSound `json:"ios,omitempty"`
	Android *NotificationSound `json:"android,omitempty"`
	Other   *NotificationSound `json:"other,omitempty"`
}

// Get 返回某平台槽的声音，nil=未设置。
func (s NotifySounds) Get(slot NotifySoundSlot) *NotificationSound {
	switch slot {
	case NotifySoundSlotIOS:
		return s.IOS
	case NotifySoundSlotAndroid:
		return s.Android
	case NotifySoundSlotOther:
		return s.Other
	}
	return nil
}

// With 返回把某平台槽替换为 sound（nil=清空）后的副本。
func (s NotifySounds) With(slot NotifySoundSlot, sound *NotificationSound) NotifySounds {
	var v *NotificationSound
	if sound != nil {
		c := *sound
		v = &c
	}
	switch slot {
	case NotifySoundSlotIOS:
		s.IOS = v
	case NotifySoundSlotAndroid:
		s.Android = v
	case NotifySoundSlotOther:
		s.Other = v
	}
	return s
}

// IsZero 报告三个槽是否都未设置。
func (s NotifySounds) IsZero() bool {
	return s.IOS == nil && s.Android == nil && s.Other == nil
}

// Clone 深拷贝三个槽。
func (s NotifySounds) Clone() NotifySounds {
	return NotifySounds{}.With(NotifySoundSlotIOS, s.IOS).
		With(NotifySoundSlotAndroid, s.Android).
		With(NotifySoundSlotOther, s.Other)
}

// PeerNotifySettings 是 peerNotifySettings 的业务层表达。各字段为指针=可选：
// nil 表示该项未设置（客户端按所属类别默认继承）。Sound/StoriesSound 按平台分槽。
type PeerNotifySettings struct {
	ShowPreviews      *bool
	Silent            *bool
	MuteUntil         *int
	StoriesMuted      *bool
	StoriesHideSender *bool
	Sound             NotifySounds
	StoriesSound      NotifySounds
}

// IsZero 报告该设置是否全部未设置（无需持久化/可视为继承默认）。
func (s PeerNotifySettings) IsZero() bool {
	return s.ShowPreviews == nil && s.Silent == nil && s.MuteUntil == nil &&
		s.StoriesMuted == nil && s.StoriesHideSender == nil &&
		s.Sound.IsZero() && s.StoriesSound.IsZero()
}

// NotifyException 是一个 peer（可含 forum 话题）的非默认通知设置，供
// account.getNotifyExceptions 列出"有自定义通知设置的会话"全局索引。
type NotifyException struct {
	Peer     Peer
	TopicID  int
	Settings PeerNotifySettings
}

// Clone 深拷贝指针字段，供内存 store 隔离。
func (s PeerNotifySettings) Clone() PeerNotifySettings {
	out := PeerNotifySettings{}
	if s.ShowPreviews != nil {
		v := *s.ShowPreviews
		out.ShowPreviews = &v
	}
	if s.Silent != nil {
		v := *s.Silent
		out.Silent = &v
	}
	if s.MuteUntil != nil {
		v := *s.MuteUntil
		out.MuteUntil = &v
	}
	if s.StoriesMuted != nil {
		v := *s.StoriesMuted
		out.StoriesMuted = &v
	}
	if s.StoriesHideSender != nil {
		v := *s.StoriesHideSender
		out.StoriesHideSender = &v
	}
	out.Sound = s.Sound.Clone()
	out.StoriesSound = s.StoriesSound.Clone()
	return out
}
