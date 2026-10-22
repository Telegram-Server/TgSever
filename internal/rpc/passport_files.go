package rpc

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func passportElementFiles(el domain.PassportElement) []domain.PassportFile {
	out := make([]domain.PassportFile, 0, len(el.Files)+len(el.Translation)+3)
	out = append(out, el.Files...)
	out = append(out, el.Translation...)
	for _, f := range []*domain.PassportFile{el.FrontSide, el.ReverseSide, el.Selfie} {
		if f != nil {
			out = append(out, *f)
		}
	}
	return out
}
func (r *Router) resolvePassportFile(ctx context.Context, userID int64, input tg.InputSecureFileClass) (domain.PassportFile, error) {
	switch in := input.(type) {
	case *tg.InputSecureFileUploaded:
		if r.deps.Files == nil || in.ID == 0 || in.Parts <= 0 || in.Parts > 32768 || len(in.FileHash) != 32 || len(in.Secret) != 32 {
			return domain.PassportFile{}, domain.ErrPassportInvalid
		}
		document, err := r.deps.Files.CreateDocumentFromUpload(ctx, domain.UploadedFileRef{OwnerUserID: userID, FileID: in.ID, Parts: in.Parts, MD5: in.MD5Checksum, Name: "passport.enc"}, domain.DocumentSpec{MimeType: "application/octet-stream", ForceFile: true})
		if err != nil {
			return domain.PassportFile{}, err
		}
		if document.Size < 32 || document.Size%16 != 0 || document.Size > 10*1024*1024+256 {
			return domain.PassportFile{}, domain.ErrPassportInvalid
		}
		return domain.PassportFile{ID: document.ID, AccessHash: document.AccessHash, Size: document.Size, DCID: document.DCID, Date: int(r.clock.Now().Unix()), Hash: base64.StdEncoding.EncodeToString(in.FileHash), Secret: append([]byte(nil), in.Secret...)}, nil
	case *tg.InputSecureFile:
		data, err := r.deps.GamesPassport.GetPassportData(ctx, userID)
		if err != nil {
			return domain.PassportFile{}, err
		}
		secret, err := r.passportSecret(ctx, userID)
		if err != nil {
			return domain.PassportFile{}, err
		}
		for _, el := range data.Elements {
			if el.SecretID != secret.ID {
				continue
			}
			for _, f := range passportElementFiles(el) {
				if f.ID == in.ID && f.AccessHash == in.AccessHash {
					return f, nil
				}
			}
		}
		return domain.PassportFile{}, domain.ErrPassportInvalid
	default:
		return domain.PassportFile{}, domain.ErrPassportInvalid
	}
}
func (r *Router) passportFileLocation(ctx context.Context, location *tg.InputSecureFileLocation) (string, bool, error) {
	userID, err := r.passportUser(ctx)
	if err != nil {
		return "", false, err
	}
	data, err := r.deps.GamesPassport.GetPassportData(ctx, userID)
	if err != nil {
		return "", false, err
	}
	secret, err := r.passportSecret(ctx, userID)
	if err != nil {
		return "", false, err
	}
	for _, el := range data.Elements {
		if el.SecretID != secret.ID {
			continue
		}
		for _, f := range passportElementFiles(el) {
			if f.ID == location.ID && f.AccessHash == location.AccessHash {
				return fmt.Sprintf("doc:%d", f.ID), true, nil
			}
		}
	}
	return "", false, nil
}
