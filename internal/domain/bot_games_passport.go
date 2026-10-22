package domain

import (
	"encoding/base64"
	"errors"
	"slices"
	"strings"
	"unicode/utf8"
)

// BotGame is the registered, immutable identity and display snapshot of a bot game.
type BotGame struct {
	ID          int64     `json:"id"`
	AccessHash  int64     `json:"access_hash"`
	BotUserID   int64     `json:"bot_user_id"`
	ShortName   string    `json:"short_name"`
	Title       string    `json:"title"`
	Description string    `json:"description"`
	Photo       Photo     `json:"photo"`
	Animation   *Document `json:"animation,omitempty"`
}

type GameScore struct {
	UserID   int64
	Score    int
	Position int
}
type GameScoreScope struct {
	GameID          int64
	ChatID          int64
	InlineOwnerID   int64
	InlineMessageID int
}

func GameHighScoreWindow(scores []GameScore, userID int64) []GameScore {
	out := make([]GameScore, 0, 8)
	center := -1
	for i := range scores {
		if scores[i].UserID == userID {
			center = i
			break
		}
	}
	for i, score := range scores {
		if i < 3 || (center >= 0 && i >= center-2 && i <= center+2) {
			score.Position = i + 1
			out = append(out, score)
		}
	}
	return out
}

var ErrGameScoreNotModified = errors.New("SCORE_NOT_MODIFIED")
var ErrPassportInvalid = errors.New("PASSPORT_DATA_INVALID")
var ErrPassportErrorsPending = errors.New("SECURE_VALUE_INVALID")

// PassportElement keeps encrypted client-provided bytes. Hashes are capabilities:
// a bot may only reject elements included in that user's explicit authorization.
type PassportElement struct {
	Type        string         `json:"type"`
	SecretID    int64          `json:"secret_id"`
	Hash        string         `json:"hash"`
	DataHash    string         `json:"data_hash,omitempty"`
	Data        string         `json:"data,omitempty"`
	PhoneNumber string         `json:"phone_number,omitempty"`
	Email       string         `json:"email,omitempty"`
	Files       []PassportFile `json:"files,omitempty"`
	FrontSide   *PassportFile  `json:"front_side,omitempty"`
	ReverseSide *PassportFile  `json:"reverse_side,omitempty"`
	Selfie      *PassportFile  `json:"selfie,omitempty"`
	Translation []PassportFile `json:"translation,omitempty"`
	// ValueTL preserves the opaque encrypted secret and exact secureValue wire representation.
	ValueTL []byte `json:"value_tl"`
}
type PassportFile struct {
	ID         int64  `json:"id"`
	AccessHash int64  `json:"access_hash"`
	Size       int64  `json:"size"`
	DCID       int    `json:"dc_id"`
	Date       int    `json:"date"`
	Hash       string `json:"hash"`
	Secret     []byte `json:"secret"`
}
type PassportError struct {
	Source      string   `json:"source"`
	Type        string   `json:"type"`
	FieldName   string   `json:"field_name,omitempty"`
	DataHash    string   `json:"data_hash,omitempty"`
	FileHash    string   `json:"file_hash,omitempty"`
	FileHashes  []string `json:"file_hashes,omitempty"`
	ElementHash string   `json:"element_hash,omitempty"`
	Message     string   `json:"message"`
}
type PassportSecret struct {
	Algo   string
	Salt   []byte
	Secret []byte
	ID     int64
}
type PassportAuthorization struct {
	BotUserID         int64             `json:"bot_user_id"`
	Scope             string            `json:"scope"`
	PublicKey         string            `json:"public_key"`
	Elements          []PassportElement `json:"elements"`
	Errors            []PassportError   `json:"errors,omitempty"`
	CredentialsData   []byte            `json:"credentials_data"`
	CredentialsHash   []byte            `json:"credentials_hash"`
	CredentialsSecret []byte            `json:"credentials_secret"`
}
type PassportUserData struct {
	Elements       []PassportElement       `json:"elements"`
	Authorizations []PassportAuthorization `json:"authorizations"`
}

func ValidPassportHash(hash string) bool {
	b, e := base64.StdEncoding.DecodeString(hash)
	return e == nil && len(b) == 32
}
func passportTypeIn(t string, types ...string) bool { return slices.Contains(types, t) }
func ValidPassportType(t string) bool {
	return passportTypeIn(t, "personal_details", "passport", "driver_license", "identity_card", "internal_passport", "address", "utility_bill", "bank_statement", "rental_agreement", "passport_registration", "temporary_registration", "phone_number", "email")
}
func ValidatePassportErrors(elements []PassportElement, errs []PassportError) error {
	if len(errs) == 0 || len(errs) > 100 {
		return ErrPassportInvalid
	}
	for _, e := range errs {
		if strings.TrimSpace(e.Message) == "" || utf8.RuneCountInString(e.Message) > 512 {
			return ErrPassportInvalid
		}
		var el *PassportElement
		for i := range elements {
			if elements[i].Type == e.Type {
				el = &elements[i]
				break
			}
		}
		if el == nil || !ValidPassportType(e.Type) {
			return ErrPassportInvalid
		}
		dataType := passportTypeIn(e.Type, "personal_details", "passport", "driver_license", "identity_card", "internal_passport", "address")
		identity := passportTypeIn(e.Type, "passport", "driver_license", "identity_card", "internal_passport")
		document := passportTypeIn(e.Type, "utility_bill", "bank_statement", "rental_agreement", "passport_registration", "temporary_registration")
		matches := func(f *PassportFile) bool { return f != nil && ValidPassportHash(e.FileHash) && f.Hash == e.FileHash }
		switch e.Source {
		case "data":
			if !dataType || e.DataHash != el.DataHash || !ValidPassportHash(e.DataHash) || !validPassportField(e.Type, e.FieldName) {
				return ErrPassportInvalid
			}
		case "front_side":
			if !identity || !matches(el.FrontSide) {
				return ErrPassportInvalid
			}
		case "reverse_side":
			if !passportTypeIn(e.Type, "driver_license", "identity_card") || !matches(el.ReverseSide) {
				return ErrPassportInvalid
			}
		case "selfie":
			if !identity || !matches(el.Selfie) {
				return ErrPassportInvalid
			}
		case "file", "files", "translation_file", "translation_files":
			translation := strings.HasPrefix(e.Source, "translation_")
			if (!translation && !document) || (translation && !identity && !document) {
				return ErrPassportInvalid
			}
			files := el.Files
			if translation {
				files = el.Translation
			}
			hashes := e.FileHashes
			if e.Source == "file" || e.Source == "translation_file" {
				hashes = []string{e.FileHash}
			}
			if len(hashes) == 0 {
				return ErrPassportInvalid
			}
			seen := map[string]bool{}
			for _, h := range hashes {
				if !ValidPassportHash(h) || seen[h] {
					return ErrPassportInvalid
				}
				seen[h] = true
				found := false
				for _, f := range files {
					if f.Hash == h {
						found = true
						break
					}
				}
				if !found {
					return ErrPassportInvalid
				}
			}
		case "unspecified":
			if !ValidPassportHash(e.ElementHash) || el.Hash != e.ElementHash {
				return ErrPassportInvalid
			}
		default:
			return ErrPassportInvalid
		}
	}
	return nil
}
func validPassportField(t, field string) bool {
	switch t {
	case "personal_details":
		return passportTypeIn(field, "first_name", "last_name", "middle_name", "birth_date", "gender", "country_code", "residence_country_code", "first_name_native", "last_name_native", "middle_name_native")
	case "address":
		return passportTypeIn(field, "street_line1", "street_line2", "city", "state", "country_code", "post_code")
	default:
		return passportTypeIn(field, "document_no", "expiry_date")
	}
}

// PassportErrorUnchanged compares only the component named by the error. Encrypted
// data fields cannot be decrypted by the server, so data_hash is the change token.
func PassportErrorUnchanged(old PassportElement, current PassportElement, e PassportError) bool {
	switch e.Source {
	case "data":
		return old.DataHash == current.DataHash
	case "front_side":
		return old.FrontSide != nil && current.FrontSide != nil && old.FrontSide.Hash == current.FrontSide.Hash
	case "reverse_side":
		return old.ReverseSide != nil && current.ReverseSide != nil && old.ReverseSide.Hash == current.ReverseSide.Hash
	case "selfie":
		return old.Selfie != nil && current.Selfie != nil && old.Selfie.Hash == current.Selfie.Hash
	case "file", "files", "translation_file", "translation_files":
		files := current.Files
		oldFiles := old.Files
		if strings.HasPrefix(e.Source, "translation_") {
			files = current.Translation
			oldFiles = old.Translation
		}
		if strings.HasSuffix(e.Source, "files") {
			if len(files) != len(oldFiles) {
				return false
			}
			for _, file := range files {
				count := 0
				for _, oldFile := range oldFiles {
					if oldFile.Hash == file.Hash {
						count++
					}
				}
				for _, currentFile := range files {
					if currentFile.Hash == file.Hash {
						count--
					}
				}
				if count != 0 {
					return false
				}
			}
			return true
		}
		for _, f := range files {
			if f.Hash == e.FileHash {
				return true
			}
		}
		return false
	default:
		return old.Hash == current.Hash
	}
}

type BotAPISendGameInput struct {
	ChatID      int64
	ShortName   string
	ReplyMarkup *MessageReplyMarkup
	Silent      bool
	ReplyTo     int
	Options     BotAPIMessageOptions
}
type BotAPIGameHighScore struct {
	Position int
	User     User
	Score    int
}
type BusinessGameMessageRef struct {
	BotUserID, ChatID, OwnerUserID int64
	MessageID                      int
	ConnectionID                   string
}
