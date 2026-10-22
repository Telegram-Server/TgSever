package rpc

import (
	"encoding/json"
	"github.com/iamxvbaba/td/tg"
	"strings"
	"telesrv/internal/domain"
)

func passportRequiredTypes(scope string) ([]tg.SecureRequiredTypeClass, error) {
	if len(scope) > 16384 {
		return nil, domain.ErrPassportInvalid
	}
	var payload struct {
		Version int               `json:"v"`
		Data    []json.RawMessage `json:"d"`
	}
	if json.Unmarshal([]byte(scope), &payload) != nil || payload.Version != 1 || len(payload.Data) == 0 || len(payload.Data) > 13 {
		return nil, domain.ErrPassportInvalid
	}
	seen := map[string]bool{}
	out := make([]tg.SecureRequiredTypeClass, 0, len(payload.Data))
	for _, raw := range payload.Data {
		r, err := passportRequirement(raw, seen, false, false, false)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
func passportScopeFlag(raw json.RawMessage) (bool, error) {
	switch string(raw) {
	case "", "false", "0":
		return false, nil
	case "true", "1":
		return true, nil
	default:
		return false, domain.ErrPassportInvalid
	}
}
func passportRequirement(raw json.RawMessage, seen map[string]bool, inGroup, inheritSelfie, inheritTranslation bool) (tg.SecureRequiredTypeClass, error) {
	var name string
	var object struct {
		Type        json.RawMessage `json:"_"`
		Selfie      json.RawMessage `json:"s"`
		Translation json.RawMessage `json:"t"`
		Native      json.RawMessage `json:"n"`
	}
	selfie, translation, native := inheritSelfie, inheritTranslation, false
	if json.Unmarshal(raw, &name) != nil {
		if json.Unmarshal(raw, &object) != nil {
			return nil, domain.ErrPassportInvalid
		}
		s, e := passportScopeFlag(object.Selfie)
		if e != nil {
			return nil, e
		}
		t, e := passportScopeFlag(object.Translation)
		if e != nil {
			return nil, e
		}
		n, e := passportScopeFlag(object.Native)
		if e != nil {
			return nil, e
		}
		selfie = selfie || s
		translation = translation || t
		native = n
		if json.Unmarshal(object.Type, &name) != nil {
			if inGroup || native {
				return nil, domain.ErrPassportInvalid
			}
			var entries []json.RawMessage
			if json.Unmarshal(object.Type, &entries) != nil || len(entries) < 2 || len(entries) > 5 {
				return nil, domain.ErrPassportInvalid
			}
			group := &tg.SecureRequiredTypeOneOf{}
			family := ""
			for _, entry := range entries {
				req, e := passportRequirement(entry, seen, true, selfie, translation)
				if e != nil {
					return nil, e
				}
				one, ok := req.(*tg.SecureRequiredType)
				if !ok {
					return nil, domain.ErrPassportInvalid
				}
				name := passportTypeName(one.Type)
				current := "identity"
				if strings.Contains("|utility_bill|bank_statement|rental_agreement|passport_registration|temporary_registration|", "|"+name+"|") {
					current = "address"
				} else if !strings.Contains("|passport|driver_license|identity_card|internal_passport|", "|"+name+"|") {
					return nil, domain.ErrPassportInvalid
				}
				if family != "" && family != current {
					return nil, domain.ErrPassportInvalid
				}
				family = current
				group.Types = append(group.Types, one)
			}
			return group, nil
		}
	}
	aliases := map[string]string{"pd": "personal_details", "pp": "passport", "dl": "driver_license", "ic": "identity_card", "ip": "internal_passport", "ad": "address", "ub": "utility_bill", "bs": "bank_statement", "ra": "rental_agreement", "pr": "passport_registration", "tr": "temporary_registration", "pn": "phone_number", "em": "email"}
	if name == "idd" || name == "add" {
		if inGroup || native {
			return nil, domain.ErrPassportInvalid
		}
		choices := []string{"pp", "dl", "ic"}
		if name == "add" {
			choices = []string{"ub", "bs", "ra"}
		}
		group := &tg.SecureRequiredTypeOneOf{}
		for _, choice := range choices {
			encoded, _ := json.Marshal(choice)
			one, e := passportRequirement(encoded, seen, true, selfie, translation)
			if e != nil {
				return nil, e
			}
			group.Types = append(group.Types, one)
		}
		return group, nil
	}
	name = aliases[name]
	if name == "" || seen[name] {
		return nil, domain.ErrPassportInvalid
	}
	seen[name] = true
	identity := name == "passport" || name == "driver_license" || name == "identity_card" || name == "internal_passport"
	document := name == "utility_bill" || name == "bank_statement" || name == "rental_agreement" || name == "passport_registration" || name == "temporary_registration"
	if selfie && !identity || translation && !identity && !document || native && name != "personal_details" {
		return nil, domain.ErrPassportInvalid
	}
	return &tg.SecureRequiredType{Type: passportTypeTG(name), SelfieRequired: selfie, TranslationRequired: translation, NativeNames: native}, nil
}
func passportRequirementsSatisfied(requirements []tg.SecureRequiredTypeClass, elements []domain.PassportElement) bool {
	allowed := map[string]bool{}
	var check func(tg.SecureRequiredTypeClass) bool
	check = func(req tg.SecureRequiredTypeClass) bool {
		switch v := req.(type) {
		case *tg.SecureRequiredType:
			for _, el := range elements {
				if el.Type != passportTypeName(v.Type) {
					continue
				}
				if v.SelfieRequired && el.Selfie == nil || v.TranslationRequired && len(el.Translation) == 0 {
					return false
				}
				return true
			}
			return false
		case *tg.SecureRequiredTypeOneOf:
			found := false
			for _, one := range v.Types {
				if check(one) {
					found = true
				}
			}
			return found
		}
		return false
	}
	var collect func(tg.SecureRequiredTypeClass)
	collect = func(req tg.SecureRequiredTypeClass) {
		switch v := req.(type) {
		case *tg.SecureRequiredType:
			allowed[passportTypeName(v.Type)] = true
		case *tg.SecureRequiredTypeOneOf:
			for _, one := range v.Types {
				collect(one)
			}
		}
	}
	for _, r := range requirements {
		collect(r)
		if !check(r) {
			return false
		}
	}
	for _, el := range elements {
		if !allowed[el.Type] {
			return false
		}
	}
	return true
}
