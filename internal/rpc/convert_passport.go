package rpc

import (
	"encoding/base64"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"telesrv/internal/domain"
)

func domainPassportSecret(in tg.SecureSecretSettings) (*domain.PassportSecret, error) {
	algo, ok := in.SecureAlgo.(*tg.SecurePasswordKdfAlgoPBKDF2HMACSHA512iter100000)
	if !ok || len(algo.Salt) != 40 || len(in.SecureSecret) != 32 || in.SecureSecretID == 0 {
		return nil, tgerr.New(400, "SECURE_SECRET_INVALID")
	}
	return &domain.PassportSecret{Algo: "pbkdf2_hmac_sha512_iter100000", Salt: append([]byte(nil), algo.Salt...), Secret: append([]byte(nil), in.SecureSecret...), ID: in.SecureSecretID}, nil
}
func tgPassportSecret(in domain.PassportSecret) tg.SecureSecretSettings {
	return tg.SecureSecretSettings{SecureAlgo: &tg.SecurePasswordKdfAlgoPBKDF2HMACSHA512iter100000{Salt: in.Salt}, SecureSecret: in.Secret, SecureSecretID: in.ID}
}
func passportTypeName(in tg.SecureValueTypeClass) string {
	switch in.(type) {
	case *tg.SecureValueTypePersonalDetails:
		return "personal_details"
	case *tg.SecureValueTypePassport:
		return "passport"
	case *tg.SecureValueTypeDriverLicense:
		return "driver_license"
	case *tg.SecureValueTypeIdentityCard:
		return "identity_card"
	case *tg.SecureValueTypeInternalPassport:
		return "internal_passport"
	case *tg.SecureValueTypeAddress:
		return "address"
	case *tg.SecureValueTypeUtilityBill:
		return "utility_bill"
	case *tg.SecureValueTypeBankStatement:
		return "bank_statement"
	case *tg.SecureValueTypeRentalAgreement:
		return "rental_agreement"
	case *tg.SecureValueTypePassportRegistration:
		return "passport_registration"
	case *tg.SecureValueTypeTemporaryRegistration:
		return "temporary_registration"
	case *tg.SecureValueTypePhone:
		return "phone_number"
	case *tg.SecureValueTypeEmail:
		return "email"
	default:
		return ""
	}
}
func passportTypeTG(in string) tg.SecureValueTypeClass {
	switch in {
	case "personal_details":
		return &tg.SecureValueTypePersonalDetails{}
	case "passport":
		return &tg.SecureValueTypePassport{}
	case "driver_license":
		return &tg.SecureValueTypeDriverLicense{}
	case "identity_card":
		return &tg.SecureValueTypeIdentityCard{}
	case "internal_passport":
		return &tg.SecureValueTypeInternalPassport{}
	case "address":
		return &tg.SecureValueTypeAddress{}
	case "utility_bill":
		return &tg.SecureValueTypeUtilityBill{}
	case "bank_statement":
		return &tg.SecureValueTypeBankStatement{}
	case "rental_agreement":
		return &tg.SecureValueTypeRentalAgreement{}
	case "passport_registration":
		return &tg.SecureValueTypePassportRegistration{}
	case "temporary_registration":
		return &tg.SecureValueTypeTemporaryRegistration{}
	case "phone_number":
		return &tg.SecureValueTypePhone{}
	case "email":
		return &tg.SecureValueTypeEmail{}
	default:
		return nil
	}
}
func tgPassportValue(el domain.PassportElement) (tg.SecureValue, error) {
	var out tg.SecureValue
	b := bin.Buffer{Buf: el.ValueTL}
	err := out.Decode(&b)
	return out, err
}
func tgPassportError(e domain.PassportError) tg.SecureValueErrorClass {
	hash := func(s string) []byte { b, _ := base64.StdEncoding.DecodeString(s); return b }
	t := passportTypeTG(e.Type)
	switch e.Source {
	case "data":
		return &tg.SecureValueErrorData{Type: t, DataHash: hash(e.DataHash), Field: e.FieldName, Text: e.Message}
	case "front_side":
		return &tg.SecureValueErrorFrontSide{Type: t, FileHash: hash(e.FileHash), Text: e.Message}
	case "reverse_side":
		return &tg.SecureValueErrorReverseSide{Type: t, FileHash: hash(e.FileHash), Text: e.Message}
	case "selfie":
		return &tg.SecureValueErrorSelfie{Type: t, FileHash: hash(e.FileHash), Text: e.Message}
	case "file":
		return &tg.SecureValueErrorFile{Type: t, FileHash: hash(e.FileHash), Text: e.Message}
	case "translation_file":
		return &tg.SecureValueErrorTranslationFile{Type: t, FileHash: hash(e.FileHash), Text: e.Message}
	case "files", "translation_files":
		hashes := make([][]byte, 0, len(e.FileHashes))
		for _, h := range e.FileHashes {
			hashes = append(hashes, hash(h))
		}
		if e.Source == "files" {
			return &tg.SecureValueErrorFiles{Type: t, FileHash: hashes, Text: e.Message}
		}
		return &tg.SecureValueErrorTranslationFiles{Type: t, FileHash: hashes, Text: e.Message}
	default:
		return &tg.SecureValueError{Type: t, Hash: hash(e.ElementHash), Text: e.Message}
	}
}
func tgPassportFile(f domain.PassportFile) *tg.SecureFile {
	hash, _ := base64.StdEncoding.DecodeString(f.Hash)
	return &tg.SecureFile{ID: f.ID, AccessHash: f.AccessHash, Size: f.Size, DCID: f.DCID, Date: f.Date, FileHash: hash, Secret: f.Secret}
}
