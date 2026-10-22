package store

import (
	"context"
	"telesrv/internal/domain"
)

// StarGiftOwnerStore is the owner lifecycle subset shared by the memory
// aggregate and the production upgrade/lifecycle coordinators.
type StarGiftOwnerStore interface {
	UpgradeStarGift(context.Context, domain.StarGiftUpgradeRequest) (domain.StarGiftUpgradeResult, error)
	StarGiftUpgradeReceipt(context.Context, int64, string) (domain.StarGiftUpgradeReceipt, bool, error)
	ConvertStarGift(context.Context, domain.StarGiftConvertRequest) (domain.StarGiftConvertResult, error)
	TransferStarGift(context.Context, domain.StarGiftTransferRequest) (domain.StarGiftTransferResult, error)
}
