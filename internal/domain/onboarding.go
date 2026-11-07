package domain

import (
	"errors"
	"slices"
	"time"
)

// 新账号默认入驻：后台配置一份入驻模板（默认联系人 + 自动加入的群和频道），任何途径新建的
// 账号（后台建号、验证码注册、密码注册、游客注册）在建号事务里按当时的模板写入一条入驻任务，
// 之后由服务端异步执行。模板是快照：修改模板只影响之后新建的账号。

const (
	// MaxOnboardingContacts 与 MaxOnboardingChannels 是模板里两份名单各自的上限。
	MaxOnboardingContacts = 50
	MaxOnboardingChannels = 50
	// OnboardingJobMaxAttempts 是一条入驻任务最多执行的次数，用完仍失败就放弃。
	OnboardingJobMaxAttempts = 5
)

var (
	// ErrOnboardingDefaultsInvalid 表示模板不合法：名单超过上限、有重复项、有不存在或已注销的账号、
	// 有不存在的群或频道。
	ErrOnboardingDefaultsInvalid = errors.New("onboarding defaults invalid")
)

// OnboardingContact 是模板里的一个默认联系人。Mutual=true 时双向添加：新账号的通讯录里有对方，
// 对方的通讯录里也加上新账号；否则只加到新账号的通讯录里。
type OnboardingContact struct {
	UserID int64
	Mutual bool
}

// OnboardingDefaults 是入驻模板。两份名单都按配置顺序保存，没有重复项。
type OnboardingDefaults struct {
	Contacts   []OnboardingContact
	ChannelIDs []int64
	// UpdatedAt 与 CommandID 由 store 在写入时记录：最近一次写入的时间与后台命令 id。
	UpdatedAt time.Time
	CommandID string
}

// Empty 表示模板里没有任何入驻项，新账号不需要入驻任务。
func (d OnboardingDefaults) Empty() bool {
	return len(d.Contacts) == 0 && len(d.ChannelIDs) == 0
}

// Clone 返回名单独立的副本；nil 名单规范成空切片。
func (d OnboardingDefaults) Clone() OnboardingDefaults {
	d.Contacts = slices.Clone(d.Contacts)
	if d.Contacts == nil {
		d.Contacts = []OnboardingContact{}
	}
	d.ChannelIDs = slices.Clone(d.ChannelIDs)
	if d.ChannelIDs == nil {
		d.ChannelIDs = []int64{}
	}
	return d
}

// ContactSignUpNotice 是写给 OwnerUserID 一方的 messageActionContactSignUp 服务消息：在
// Owner 与 From 的私聊里，以 From 为发送者，显示为"From 加入了 …"。只写 Owner 一侧，From
// 那边什么都不出现。Silent=true 时客户端不弹通知（Owner 关了"联系人加入时通知"）。
type ContactSignUpNotice struct {
	OwnerUserID int64
	FromUserID  int64
	Silent      bool
	Date        int
}

// OnboardingJob 是一个新账号的入驻任务：建号时从模板复制的名单，以及执行情况。
type OnboardingJob struct {
	UserID     int64
	Contacts   []OnboardingContact
	ChannelIDs []int64
	// Attempts 是已经失败的次数，LastError 是最近一次失败的原因。
	Attempts  int
	LastError string
	CreatedAt time.Time
}
