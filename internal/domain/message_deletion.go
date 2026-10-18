package domain

// DeletionSource 是删除操作的发起途径。
type DeletionSource string

const (
	// DeletionSourceUser 是用户按消息 ID 主动删除。
	DeletionSourceUser DeletionSource = "user"
	// DeletionSourceHistoryClear 是清空（或按日期删除）会话历史。
	DeletionSourceHistoryClear DeletionSource = "history_clear"
	// DeletionSourceSavedHistory 是清空收藏夹中的一个子会话。
	DeletionSourceSavedHistory DeletionSource = "saved_history"
	// DeletionSourceTTL 是消息到达自动删除时间后由服务端删除。
	DeletionSourceTTL DeletionSource = "ttl"
	// DeletionSourceAdmin 是后台操作员删除。
	DeletionSourceAdmin DeletionSource = "admin"
	// DeletionSourceBot 是机器人通过 Bot API 删除。
	DeletionSourceBot DeletionSource = "bot"
)

// DeletionActor 是删除操作的发起方类型。
type DeletionActor string

const (
	DeletionActorUser DeletionActor = "user"
	// DeletionActorSystem 是服务端自动发起，没有对应的用户。
	DeletionActorSystem DeletionActor = "system"
	// DeletionActorOperator 是后台操作员，由 OperatorID 标识。
	DeletionActorOperator DeletionActor = "operator"
	// DeletionActorBot 是机器人账号，ActorUserID 为机器人的用户 ID。
	DeletionActorBot DeletionActor = "bot"
)

// DeletionScope 是一次删除影响的范围。
type DeletionScope string

const (
	// DeletionScopeSelf 只删除发起方自己那一侧的副本。
	DeletionScopeSelf DeletionScope = "self"
	// DeletionScopeBoth 同时删除私聊双方的副本（revoke）。
	DeletionScopeBoth DeletionScope = "both"
)

// PrivateDeletionScope 返回私聊删除的范围：revoke 同时删除双方副本。
func PrivateDeletionScope(revoke bool) DeletionScope {
	if revoke {
		return DeletionScopeBoth
	}
	return DeletionScopeSelf
}

// DeletionAudit 描述一次删除操作是谁、从哪发起的，随删除事务写入删除记录。
type DeletionAudit struct {
	Source          DeletionSource
	ActorType       DeletionActor
	ActorUserID     int64
	OriginAuthKeyID [8]byte
	OriginSessionID int64
	OperatorID      string
	CommandID       string
	Reason          string
}

// OperatorDeletionAudit 是后台操作员通过 admin command 发起删除时的归属。
func OperatorDeletionAudit(operatorID, commandID, reason string) DeletionAudit {
	return DeletionAudit{
		Source:     DeletionSourceAdmin,
		ActorType:  DeletionActorOperator,
		OperatorID: operatorID,
		CommandID:  commandID,
		Reason:     reason,
	}
}

// UserDeletionAudit 是用户从某个设备会话自己发起删除时的归属。
func UserDeletionAudit(source DeletionSource, userID int64, authKeyID [8]byte, sessionID int64) DeletionAudit {
	return DeletionAudit{
		Source:          source,
		ActorType:       DeletionActorUser,
		ActorUserID:     userID,
		OriginAuthKeyID: authKeyID,
		OriginSessionID: sessionID,
	}
}

// BotDeletionAudit 是机器人通过 Bot API 发起删除时的归属。
func BotDeletionAudit(botID int64) DeletionAudit {
	return DeletionAudit{Source: DeletionSourceBot, ActorType: DeletionActorBot, ActorUserID: botID}
}

// SystemDeletionAudit 是服务端自动发起删除时的归属。
func SystemDeletionAudit(source DeletionSource) DeletionAudit {
	return DeletionAudit{Source: source, ActorType: DeletionActorSystem}
}

// OrUser 在调用方未显式声明归属时，回落为请求账号从当前会话发起的删除。
func (a DeletionAudit) OrUser(source DeletionSource, userID int64, authKeyID [8]byte, sessionID int64) DeletionAudit {
	if a.Source != "" {
		return a
	}
	return UserDeletionAudit(source, userID, authKeyID, sessionID)
}

// MessageDeletion 是删除记录中的一行：一次删除操作移除的一个消息副本。
type MessageDeletion struct {
	ID          int64
	BatchID     int64
	Date        int
	Source      DeletionSource
	Scope       DeletionScope
	ActorType   DeletionActor
	ActorUserID int64
	// OwnerUserID 是被删副本所属的账号（私聊 message box 的 owner）。
	OwnerUserID  int64
	Peer         Peer
	MessageID    int
	SenderUserID int64
	MessageDate  int
	Body         string
	// OriginAuthKeyID/OriginSessionID 是发起删除的设备会话；系统发起时为零。
	OriginAuthKeyID [8]byte
	OriginSessionID int64
	// OperatorID/CommandID/Reason 是后台删除的操作员、admin command 与原因。
	OperatorID string
	CommandID  string
	Reason     string
	// OriginDevice 是删除时刻该会话的设备快照；设备之后退出登录也不受影响。
	OriginDevice MessageDeletionDevice
}

// MessageDeletionDevice 是发起删除的设备信息（取自 authorizations）。
type MessageDeletionDevice struct {
	DeviceModel string
	Platform    string
	IP          string
}

// MessageDeletionFilter 筛选删除记录；零值字段不参与筛选，结果按 ID 倒序。
type MessageDeletionFilter struct {
	ActorUserID  int64
	SenderUserID int64
	Limit        int
}
