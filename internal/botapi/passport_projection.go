package botapi

import (
	"encoding/base64"
	"strconv"
	"telesrv/internal/domain"
)

func init() { registerMessageProjection(projectPassportData) }
func projectPassportFile(file domain.PassportFile) map[string]any {
	key := "doc:" + strconv.FormatInt(file.ID, 10)
	id := encodeBotAPIFileID(key)
	return map[string]any{"file_id": id, "file_unique_id": id, "file_size": file.Size, "file_date": file.Date}
}
func projectPassportData(msg domain.Message, out map[string]any) {
	if msg.Media == nil || msg.Media.ServiceAction == nil || msg.Media.ServiceAction.PassportData == nil {
		return
	}
	a := msg.Media.ServiceAction.PassportData
	// Service data is a bot-received object, never a copied/forwarded user's Passport.
	if msg.Out || msg.OwnerUserID != a.BotUserID {
		return
	}
	elements := make([]map[string]any, 0, len(a.Elements))
	for _, el := range a.Elements {
		item := map[string]any{"type": el.Type, "hash": el.Hash}
		if el.Data != "" {
			item["data"] = el.Data
		}
		if el.PhoneNumber != "" {
			item["phone_number"] = el.PhoneNumber
		}
		if el.Email != "" {
			item["email"] = el.Email
		}
		if len(el.Files) > 0 {
			files := make([]map[string]any, 0, len(el.Files))
			for _, f := range el.Files {
				files = append(files, projectPassportFile(f))
			}
			item["files"] = files
		}
		if len(el.Translation) > 0 {
			files := make([]map[string]any, 0, len(el.Translation))
			for _, f := range el.Translation {
				files = append(files, projectPassportFile(f))
			}
			item["translation"] = files
		}
		if el.FrontSide != nil {
			item["front_side"] = projectPassportFile(*el.FrontSide)
		}
		if el.ReverseSide != nil {
			item["reverse_side"] = projectPassportFile(*el.ReverseSide)
		}
		if el.Selfie != nil {
			item["selfie"] = projectPassportFile(*el.Selfie)
		}
		elements = append(elements, item)
	}
	out["passport_data"] = map[string]any{"data": elements, "credentials": map[string]any{"data": base64.StdEncoding.EncodeToString(a.CredentialsData), "hash": base64.StdEncoding.EncodeToString(a.CredentialsHash), "secret": base64.StdEncoding.EncodeToString(a.CredentialsSecret)}}
	delete(out, "text")
}
