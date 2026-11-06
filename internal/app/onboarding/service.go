// Package onboarding 是新账号默认入驻：后台配置的入驻模板（默认联系人、自动加入的群和频道），
// 以及按建号时写入的入驻任务把它们落到新账号上。
//
// 任务由 store 在建号事务里写入（见 store.OnboardingStore），由服务端的调度器领取后调用
// Execute 执行；update 推送由调度器根据 Execute 的结果完成。入驻是系统动作：不经过账号权限，
// 开启了入群审批的群也直接加入。
package onboarding

import (
	"context"
	"errors"
	"fmt"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// Users 按 id 读取账号。
type Users interface {
	ByID(ctx context.Context, id int64) (domain.User, bool, error)
}

// Contacts 写入联系人（生产里是 app/contacts.Service）。
type Contacts interface {
	AddContact(ctx context.Context, userID int64, input domain.ContactInput) (domain.Contact, error)
}

// Channels 读取群和频道并直接加入（生产里是 app/channels.Service）。
type Channels interface {
	GetChannelByID(ctx context.Context, channelID int64) (domain.Channel, error)
	AdmitToChannel(ctx context.Context, userID, channelID int64, date int) (domain.CreateChannelResult, error)
}

// Service 是入驻模板的读写与入驻任务的执行。
type Service struct {
	store    store.OnboardingStore
	users    Users
	contacts Contacts
	channels Channels
	now      func() time.Time
	// retryBackoff 见 WithRetryBackoff。
	retryBackoff time.Duration
}

// NewService 创建入驻服务。
func NewService(st store.OnboardingStore, users Users, contacts Contacts, channels Channels) *Service {
	return &Service{store: st, users: users, contacts: contacts, channels: channels, now: time.Now, retryBackoff: defaultRetryBackoff}
}

// AddedContact 是写入的一条联系人：Owner 的通讯录里加上了 Contact.User。
type AddedContact struct {
	OwnerUserID int64
	Contact     domain.Contact
}

// SkippedKind 是被跳过的入驻项的种类。
type SkippedKind string

const (
	SkippedContact SkippedKind = "contact"
	SkippedChannel SkippedKind = "channel"
)

// Skipped 是一项因为目标不可用而跳过的入驻项（不是失败，任务不会因此重试）。
type Skipped struct {
	Kind   SkippedKind
	ID     int64
	Reason string
}

// Result 是一次执行落下的变化，调度器据此推送 update 并记日志。
type Result struct {
	Contacts []AddedContact
	Joins    []domain.CreateChannelResult
	Skipped  []Skipped
}

// Execute 执行一条入驻任务。
func (s *Service) Execute(ctx context.Context, job domain.OnboardingJob) (Result, error) {
	var res Result
	newcomer, found, err := s.users.ByID(ctx, job.UserID)
	if err != nil {
		return res, fmt.Errorf("onboarding: read new account %d: %w", job.UserID, err)
	}
	if !found || newcomer.Deleted {
		return res, nil
	}
	for _, c := range job.Contacts {
		target, found, err := s.users.ByID(ctx, c.UserID)
		if err != nil {
			return res, fmt.Errorf("onboarding: read default contact %d: %w", c.UserID, err)
		}
		if !found || target.Deleted {
			res.Skipped = append(res.Skipped, Skipped{Kind: SkippedContact, ID: c.UserID, Reason: "account not found or deleted"})
			continue
		}
		added, err := s.addContact(ctx, newcomer.ID, target)
		if err != nil {
			return res, err
		}
		res.Contacts = append(res.Contacts, added)
		if c.Mutual {
			back, err := s.addContact(ctx, target.ID, newcomer)
			if err != nil {
				return res, err
			}
			res.Contacts = append(res.Contacts, back)
		}
	}
	for _, channelID := range job.ChannelIDs {
		joined, err := s.channels.AdmitToChannel(ctx, newcomer.ID, channelID, int(s.now().Unix()))
		if reason, skip := channelSkipReason(err); skip {
			res.Skipped = append(res.Skipped, Skipped{Kind: SkippedChannel, ID: channelID, Reason: reason})
			continue
		}
		if err != nil {
			return res, fmt.Errorf("onboarding: admit %d to channel %d: %w", newcomer.ID, channelID, err)
		}
		res.Joins = append(res.Joins, joined)
	}
	return res, nil
}

// channelSkipReason 判断加入失败是不是"这个群加不了"：群不存在或已删除、已经在群里（任务重跑）、
// 被群封禁、群已满、群类型不支持。这些跳过这一项；其他错误让整个任务重试。
func channelSkipReason(err error) (string, bool) {
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, domain.ErrUserAlreadyParticipant):
		return "already a member", true
	case errors.Is(err, domain.ErrChannelUserBanned), errors.Is(err, domain.ErrUserKicked):
		return "banned in the channel", true
	case errors.Is(err, domain.ErrUsersTooMuch):
		return "the channel is full", true
	case errors.Is(err, domain.ErrChannelInvalid), errors.Is(err, domain.ErrChannelPrivate),
		errors.Is(err, domain.ErrChannelMonoforumUnsupported):
		return "channel not found or not joinable", true
	}
	return "", false
}

// addContact 把 target 以它自己的名字加进 owner 的通讯录。不带号码：号码是否可见仍由 target 的
// 隐私设置决定。
func (s *Service) addContact(ctx context.Context, ownerUserID int64, target domain.User) (AddedContact, error) {
	contact, err := s.contacts.AddContact(ctx, ownerUserID, domain.ContactInput{
		ContactUserID: target.ID,
		FirstName:     target.FirstName,
		LastName:      target.LastName,
	})
	if err != nil {
		return AddedContact{}, fmt.Errorf("onboarding: add contact %d to %d: %w", target.ID, ownerUserID, err)
	}
	return AddedContact{OwnerUserID: ownerUserID, Contact: contact}, nil
}
