package rpc

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"strings"
	"telesrv/internal/domain"
)

func (r *Router) registerPassport(d *tlprofile.Dispatcher) {
	registerRPC[*tg.AccountSaveSecureValueRequest](d, tlprofile.SemanticMethodAccountSaveSecureValue, func(ctx context.Context, q *tg.AccountSaveSecureValueRequest) (any, error) {
		return r.onAccountSaveSecureValue(ctx, q)
	})
	registerRPC[*tg.AccountGetAllSecureValuesRequest](d, tlprofile.SemanticMethodAccountGetAllSecureValues, func(ctx context.Context, _ *tg.AccountGetAllSecureValuesRequest) (any, error) {
		return r.passportValues(ctx, nil)
	})
	registerRPC[*tg.AccountGetSecureValueRequest](d, tlprofile.SemanticMethodAccountGetSecureValue, func(ctx context.Context, q *tg.AccountGetSecureValueRequest) (any, error) {
		return r.passportValues(ctx, q.Types)
	})
	registerRPC[*tg.AccountDeleteSecureValueRequest](d, tlprofile.SemanticMethodAccountDeleteSecureValue, func(ctx context.Context, q *tg.AccountDeleteSecureValueRequest) (any, error) {
		userID, err := r.passportUser(ctx)
		if err != nil {
			return nil, err
		}
		types := make([]string, 0, len(q.Types))
		for _, t := range q.Types {
			name := passportTypeName(t)
			if name == "" {
				return false, domain.ErrPassportInvalid
			}
			types = append(types, name)
		}
		err = r.deps.GamesPassport.DeletePassportElements(ctx, userID, types)
		return err == nil, err
	})
	registerRPC[*tg.AccountGetAuthorizationFormRequest](d, tlprofile.SemanticMethodAccountGetAuthorizationForm, func(ctx context.Context, q *tg.AccountGetAuthorizationFormRequest) (any, error) {
		return r.onAccountGetAuthorizationForm(ctx, q)
	})
	registerRPC[*tg.AccountAcceptAuthorizationRequest](d, tlprofile.SemanticMethodAccountAcceptAuthorization, func(ctx context.Context, q *tg.AccountAcceptAuthorizationRequest) (any, error) {
		return r.onAccountAcceptAuthorization(ctx, q)
	})
}
func (r *Router) passportUser(ctx context.Context) (int64, error) {
	id, _, err := r.currentUserID(ctx)
	if err != nil {
		return 0, err
	}
	if id <= 0 || r.userIsBot(ctx, id) {
		return 0, tgerr.New(400, "USER_BOT_INVALID")
	}
	if r.deps.GamesPassport == nil {
		return 0, internalErr()
	}
	return id, nil
}
func (r *Router) passportSecret(ctx context.Context, id int64) (*domain.PassportSecret, error) {
	account, ok := r.deps.Account.(interface {
		GetPasswordWithoutRefresh(context.Context, int64) (domain.PasswordSettings, error)
	})
	if !ok {
		return nil, internalErr()
	}
	settings, err := account.GetPasswordWithoutRefresh(ctx, id)
	if err != nil {
		return nil, err
	}
	if !settings.HasPassword || settings.SecureSettings == nil {
		return nil, tgerr.New(400, "SECURE_SECRET_REQUIRED")
	}
	return settings.SecureSettings, nil
}
func (r *Router) passportValues(ctx context.Context, types []tg.SecureValueTypeClass) ([]tg.SecureValue, error) {
	id, err := r.passportUser(ctx)
	if err != nil {
		return nil, err
	}
	data, err := r.deps.GamesPassport.GetPassportData(ctx, id)
	if err != nil {
		return nil, err
	}
	out := []tg.SecureValue{}
	if len(data.Elements) == 0 {
		return out, nil
	}
	secret, err := r.passportSecret(ctx, id)
	if err != nil {
		return out, nil
	}
	wanted := map[string]bool{}
	for _, t := range types {
		name := passportTypeName(t)
		if name == "" {
			return nil, domain.ErrPassportInvalid
		}
		wanted[name] = true
	}
	for _, el := range data.Elements {
		if el.SecretID != secret.ID || len(types) > 0 && !wanted[el.Type] {
			continue
		}
		value, err := tgPassportValue(el)
		if err != nil {
			return nil, err
		}
		out = append(out, value)
	}
	return out, nil
}
func (r *Router) onAccountSaveSecureValue(ctx context.Context, q *tg.AccountSaveSecureValueRequest) (*tg.SecureValue, error) {
	id, err := r.passportUser(ctx)
	if err != nil {
		return nil, err
	}
	secret, err := r.passportSecret(ctx, id)
	if err != nil {
		return nil, err
	}
	if q.SecureSecretID != secret.ID {
		return nil, tgerr.New(400, "SECURE_SECRET_INVALID")
	}
	t := passportTypeName(q.Value.Type)
	if t == "" {
		return nil, domain.ErrPassportInvalid
	}
	if err = validatePassportInputShape(t, q.Value); err != nil {
		return nil, err
	}
	el := domain.PassportElement{Type: t, SecretID: secret.ID}
	value := tg.SecureValue{Type: q.Value.Type}
	if data, ok := q.Value.GetData(); ok {
		if len(data.Data) < 32 || len(data.Data)%16 != 0 || len(data.Data) > 1024*1024 || len(data.DataHash) != 32 || len(data.Secret) != 32 {
			return nil, domain.ErrPassportInvalid
		}
		value.SetData(data)
		el.Data = base64.StdEncoding.EncodeToString(data.Data)
		el.DataHash = base64.StdEncoding.EncodeToString(data.DataHash)
	}
	// Phone/email must already be verified by the account's authenticated identity.
	// An unverified arbitrary plaintext is never admitted as a Passport value.
	if plain, ok := q.Value.GetPlainData(); ok {
		user, found, e := r.deps.Users.ByID(ctx, id, id)
		if e != nil {
			return nil, e
		}
		if !found {
			return nil, userIDInvalidErr()
		}
		switch p := plain.(type) {
		case *tg.SecurePlainPhone:
			if t != "phone_number" || strings.TrimPrefix(p.Phone, "+") != strings.TrimPrefix(user.Phone, "+") {
				return nil, tgerr.New(400, "PHONE_NUMBER_UNOCCUPIED")
			}
			el.PhoneNumber = p.Phone
		case *tg.SecurePlainEmail:
			account, ok := r.deps.Account.(interface {
				GetPasswordWithoutRefresh(context.Context, int64) (domain.PasswordSettings, error)
			})
			if !ok {
				return nil, internalErr()
			}
			settings, e := account.GetPasswordWithoutRefresh(ctx, id)
			if e != nil {
				return nil, e
			}
			if t != "email" || p.Email == "" || !strings.EqualFold(p.Email, settings.LoginEmail) {
				return nil, tgerr.New(400, "EMAIL_UNCONFIRMED")
			}
			el.Email = p.Email
		default:
			return nil, domain.ErrPassportInvalid
		}
		value.SetPlainData(plain)
	}
	for _, item := range []struct {
		in     tg.InputSecureFileClass
		assign func(*domain.PassportFile, tg.SecureFileClass)
	}{
		{q.Value.FrontSide, func(f *domain.PassportFile, v tg.SecureFileClass) { el.FrontSide = f; value.SetFrontSide(v) }},
		{q.Value.ReverseSide, func(f *domain.PassportFile, v tg.SecureFileClass) { el.ReverseSide = f; value.SetReverseSide(v) }},
		{q.Value.Selfie, func(f *domain.PassportFile, v tg.SecureFileClass) { el.Selfie = f; value.SetSelfie(v) }},
	} {
		if item.in == nil {
			continue
		}
		file, e := r.resolvePassportFile(ctx, id, item.in)
		if e != nil {
			return nil, e
		}
		item.assign(&file, tgPassportFile(file))
	}
	if len(q.Value.Files) > 0 {
		files := make([]tg.SecureFileClass, 0, len(q.Value.Files))
		for _, in := range q.Value.Files {
			file, e := r.resolvePassportFile(ctx, id, in)
			if e != nil {
				return nil, e
			}
			el.Files = append(el.Files, file)
			files = append(files, tgPassportFile(file))
		}
		value.SetFiles(files)
	}
	if len(q.Value.Translation) > 0 {
		files := make([]tg.SecureFileClass, 0, len(q.Value.Translation))
		for _, in := range q.Value.Translation {
			file, e := r.resolvePassportFile(ctx, id, in)
			if e != nil {
				return nil, e
			}
			el.Translation = append(el.Translation, file)
			files = append(files, tgPassportFile(file))
		}
		value.SetTranslation(files)
	}
	var buffer bin.Buffer
	if err = value.Encode(&buffer); err != nil {
		return nil, err
	}
	hash := sha256.Sum256(buffer.Buf)
	value.Hash = hash[:]
	el.Hash = base64.StdEncoding.EncodeToString(hash[:])
	buffer.Buf = buffer.Buf[:0]
	if err = value.Encode(&buffer); err != nil {
		return nil, err
	}
	el.ValueTL = buffer.Copy()
	if err = r.deps.GamesPassport.SavePassportElement(ctx, id, el); err != nil {
		return nil, err
	}
	return &value, nil
}
func validatePassportInputShape(t string, v tg.InputSecureValue) error {
	data := !v.Data.Zero()
	plain := v.PlainData != nil
	front := v.FrontSide != nil
	reverse := v.ReverseSide != nil
	selfie := v.Selfie != nil
	dataType := t == "personal_details" || t == "address"
	identity := t == "passport" || t == "driver_license" || t == "identity_card" || t == "internal_passport"
	document := t == "utility_bill" || t == "bank_statement" || t == "rental_agreement" || t == "passport_registration" || t == "temporary_registration"
	if len(v.Files) > 20 || len(v.Translation) > 20 {
		return domain.ErrPassportInvalid
	}
	if dataType {
		if !data || plain || front || reverse || selfie || len(v.Files) > 0 || len(v.Translation) > 0 {
			return domain.ErrPassportInvalid
		}
		return nil
	}
	if identity {
		if !data || !front || plain || len(v.Files) > 0 || reverse && (t != "driver_license" && t != "identity_card") {
			return domain.ErrPassportInvalid
		}
		return nil
	}
	if document {
		if data || plain || front || reverse || selfie || len(v.Files) == 0 {
			return domain.ErrPassportInvalid
		}
		return nil
	}
	if !plain || data || front || reverse || selfie || len(v.Files) > 0 || len(v.Translation) > 0 {
		return domain.ErrPassportInvalid
	}
	return nil
}
func (r *Router) checkPassportBotKey(ctx context.Context, botID int64, key string) (int, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return 0, err
	}
	normalized, size, err := domain.NormalizePassportPublicKey(key)
	if err != nil {
		return 0, tgerr.New(400, "PUBLIC_KEY_REQUIRED")
	}
	stored, err := r.deps.GamesPassport.PassportPublicKey(ctx, botID)
	if err != nil {
		return 0, err
	}
	if stored == "" || stored != normalized {
		return 0, tgerr.New(400, "PUBLIC_KEY_REQUIRED")
	}
	return size, nil
}
func (r *Router) onAccountGetAuthorizationForm(ctx context.Context, q *tg.AccountGetAuthorizationFormRequest) (*tg.AccountAuthorizationForm, error) {
	userID, err := r.passportUser(ctx)
	if err != nil {
		return nil, err
	}
	if _, err = r.checkPassportBotKey(ctx, q.BotID, q.PublicKey); err != nil {
		return nil, err
	}
	required, err := passportRequiredTypes(q.Scope)
	if err != nil {
		return nil, err
	}
	values, err := r.passportValues(ctx, nil)
	if err != nil {
		return nil, err
	}
	bot, _, err := r.deps.Users.ByID(ctx, userID, q.BotID)
	if err != nil {
		return nil, err
	}
	out := &tg.AccountAuthorizationForm{RequiredTypes: required, Values: values, Errors: []tg.SecureValueErrorClass{}, Users: []tg.UserClass{tgUser(bot)}}
	data, err := r.deps.GamesPassport.GetPassportData(ctx, userID)
	if err != nil {
		return nil, err
	}
	for _, a := range data.Authorizations {
		if a.BotUserID == q.BotID {
			for _, e := range a.Errors {
				for _, old := range a.Elements {
					if old.Type != e.Type {
						continue
					}
					for _, current := range data.Elements {
						if current.Type == e.Type && domain.PassportErrorUnchanged(old, current, e) {
							out.Errors = append(out.Errors, tgPassportError(e))
						}
					}
				}
			}
		}
	}
	return out, nil
}
func (r *Router) onAccountAcceptAuthorization(ctx context.Context, q *tg.AccountAcceptAuthorizationRequest) (bool, error) {
	userID, err := r.passportUser(ctx)
	if err != nil {
		return false, err
	}
	size, err := r.checkPassportBotKey(ctx, q.BotID, q.PublicKey)
	if err != nil {
		return false, err
	}
	required, err := passportRequiredTypes(q.Scope)
	if err != nil {
		return false, err
	}
	secret, err := r.passportSecret(ctx, userID)
	if err != nil {
		return false, err
	}
	if len(q.Credentials.Data) < 32 || len(q.Credentials.Data)%16 != 0 || len(q.Credentials.Data) > 1024*1024 || len(q.Credentials.Hash) != 32 || len(q.Credentials.Secret) != size {
		return false, domain.ErrPassportInvalid
	}
	data, err := r.deps.GamesPassport.GetPassportData(ctx, userID)
	if err != nil {
		return false, err
	}
	a := domain.PassportAuthorization{BotUserID: q.BotID, Scope: q.Scope, PublicKey: q.PublicKey, CredentialsData: q.Credentials.Data, CredentialsHash: q.Credentials.Hash, CredentialsSecret: q.Credentials.Secret}
	for _, h := range q.ValueHashes {
		found := false
		for _, el := range data.Elements {
			if el.SecretID == secret.ID && el.Type == passportTypeName(h.Type) {
				hash, _ := base64.StdEncoding.DecodeString(el.Hash)
				if bytes.Equal(hash, h.Hash) {
					a.Elements = append(a.Elements, el)
					found = true
					break
				}
			}
		}
		if !found {
			return false, domain.ErrPassportInvalid
		}
	}
	if !passportRequirementsSatisfied(required, a.Elements) {
		return false, domain.ErrPassportInvalid
	}
	_, err = r.deps.Messages.SendPrivateText(ctx, userID, domain.SendPrivateTextRequest{SenderUserID: userID, RecipientUserID: q.BotID, RandomID: randomNonZeroInt64(), Date: int(r.clock.Now().Unix()), Media: &domain.MessageMedia{Kind: domain.MessageMediaKindService, ServiceAction: &domain.MessageServiceAction{Kind: domain.MessageServiceActionPassportData, PassportData: &a}}})
	return err == nil, err
}
func (r *Router) BotAPISetPassportDataErrors(ctx context.Context, botID, userID int64, errs []domain.PassportError) (bool, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return false, err
	}
	if r.deps.GamesPassport == nil {
		return false, domain.ErrPassportInvalid
	}
	secret, err := r.passportSecret(ctx, userID)
	if err != nil {
		return false, domain.ErrPassportInvalid
	}
	data, err := r.deps.GamesPassport.GetPassportData(ctx, userID)
	if err != nil {
		return false, err
	}
	for _, e := range errs {
		found := false
		for _, el := range data.Elements {
			if el.Type == e.Type && el.SecretID == secret.ID {
				found = true
				break
			}
		}
		if !found {
			return false, domain.ErrPassportInvalid
		}
	}
	if err = r.deps.GamesPassport.SetPassportErrors(ctx, userID, botID, errs); err != nil {
		return false, err
	}
	return true, nil
}
