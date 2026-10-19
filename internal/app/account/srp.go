package account

import (
	"errors"

	"telesrv/internal/domain"
	"telesrv/internal/srp"
)

// 本文件只是两步验证到共享 SRP 包(internal/srp)的适配层:负责 domain 类型
// 与 srp 类型之间的转换,并把 srp 的哨兵错误映射成两步验证一直以来返回的
// domain 错误。SRP 数学本身不在这里。

const (
	// passwordHashSize 是 2FA 相关定长随机值(如 secure_random)的长度,与 SRP 大数同长。
	passwordHashSize = srp.HashSize
)

func defaultPasswordAlgo() domain.PasswordKDFAlgo {
	return domainAlgo(srp.DefaultAlgo())
}

func defaultSecureAlgo() domain.SecurePasswordKDFAlgo {
	return domain.SecurePasswordKDFAlgo{
		Kind: "pbkdf2_hmac_sha512_iter100000",
		Salt: []byte{0x7D, 0x04, 0xB3, 0x4B, 0x94, 0x82, 0x8C, 0x3D},
	}
}

func domainAlgo(a srp.Algo) domain.PasswordKDFAlgo {
	return domain.PasswordKDFAlgo{Salt1: a.Salt1, Salt2: a.Salt2, G: a.G, P: a.P}
}

func makeSRPChallenge(verifier []byte) (secret, b []byte, err error) {
	secret, b, err = srp.NewChallenge(verifier)
	if errors.Is(err, srp.ErrInvalidVerifier) {
		return nil, nil, domain.ErrPasswordHashInvalid
	}
	if err != nil {
		// 其余失败(随机源等)是内部错误,不映射成面向客户端的 2FA 错误。
		return nil, nil, err
	}
	return secret, b, nil
}

func checkSRP(settings domain.PasswordSettings, check domain.PasswordCheck) error {
	if check.Empty {
		if settings.HasPassword {
			return domain.ErrPasswordHashInvalid
		}
		return nil
	}
	if !settings.HasPassword {
		return domain.ErrPasswordHashInvalid
	}
	if settings.SRPID == 0 || settings.SRPID != check.SRPID {
		return domain.ErrSRPIDInvalid
	}
	salt1 := settings.NewAlgo.Salt1
	if settings.CurrentAlgo != nil {
		salt1 = settings.CurrentAlgo.Salt1
	}
	return mapSRPError(srp.Verify(srp.Challenge{
		Verifier: settings.SRPVerifier,
		Secret:   settings.SRPBSecret,
		B:        settings.SRPB,
		Salt1:    salt1,
	}, check.A, check.M1))
}

func validateNewPasswordSettings(in domain.PasswordInputSettings) error {
	var algo *srp.Algo
	if in.NewAlgo != nil {
		algo = &srp.Algo{Salt1: in.NewAlgo.Salt1, Salt2: in.NewAlgo.Salt2, G: in.NewAlgo.G, P: in.NewAlgo.P}
	}
	return mapSRPError(srp.ValidateNewPassword(algo, in.NewPasswordHash))
}

// mapSRPError 把 srp 包的哨兵错误映射成 2FA 的 domain 错误;其他错误(如随机源失败)原样返回。
func mapSRPError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, srp.ErrIncompleteChallenge):
		return domain.ErrSRPPasswordChanged
	case errors.Is(err, srp.ErrInvalidVerifier), errors.Is(err, srp.ErrInvalidProof):
		return domain.ErrPasswordHashInvalid
	case errors.Is(err, srp.ErrNewSettingsMissing):
		return domain.ErrNewSettingsInvalid
	case errors.Is(err, srp.ErrNewAlgoInvalid):
		return domain.ErrNewSaltInvalid
	default:
		return err
	}
}
