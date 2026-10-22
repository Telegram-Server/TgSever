package botapi

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestStoryInputPreservesOfficialContentAndAreas(t *testing.T) {
	position := map[string]any{"x_percentage": 50, "y_percentage": 50, "width_percentage": 20, "height_percentage": 10, "rotation_angle": 45, "corner_radius_percentage": 3}
	areas := []map[string]any{
		{"position": position, "type": map[string]any{"type": "location", "latitude": 1.5, "longitude": 2.5, "address": map[string]any{"country_code": "US", "city": "Boston"}}},
		{"position": position, "type": map[string]any{"type": "suggested_reaction", "reaction_type": map[string]any{"type": "custom_emoji", "custom_emoji_id": "123"}, "is_dark": true, "is_flipped": true}},
		{"position": position, "type": map[string]any{"type": "link", "url": "tg://resolve?domain=telegram"}},
		{"position": position, "type": map[string]any{"type": "weather", "temperature": 23.5, "emoji": "☀️", "background_color": uint32(0xff001122)}},
		{"position": position, "type": map[string]any{"type": "unique_gift", "name": "PlushPepe-123"}},
	}
	encoded, _ := json.Marshal(areas)
	input, err := storyInputFromAPI("post", map[string]string{
		"business_connection_id": "opaque", "active_period": "21600", "content": `{"type":"video","video":"attach://movie","duration":1.25,"cover_frame_timestamp":0.5,"is_animation":true}`,
		"caption": "<b>A😀B</b>", "parse_mode": "HTML", "areas": string(encoded), "post_to_chat_page": "true", "protect_content": "true",
	}, map[string]uploadedFile{"movie": {Name: "story.mp4", MimeType: "video/mp4", Bytes: []byte("new-upload")}})
	if err != nil {
		t.Fatal(err)
	}
	if input.Caption != "A😀B" || len(input.Entities) != 1 || input.Entities[0].Length != 4 || input.Content.Duration != 1.25 || input.Content.CoverFrameTimestamp != .5 || !input.Content.IsAnimation || !input.PostToChatPage || !input.ProtectContent {
		t.Fatalf("input = %+v", input)
	}
	if len(input.Areas) != 5 || input.Areas[0].GeoAddress.City != "Boston" || input.Areas[1].Reaction.DocumentID != 123 || !input.Areas[1].Dark || !input.Areas[1].Flipped || input.Areas[2].URL != "tg://resolve?domain=telegram" || input.Areas[3].Color != 0xff001122 || input.Areas[4].StarGiftSlug != "PlushPepe-123" {
		t.Fatalf("areas = %+v", input.Areas)
	}
	input.Areas[2].URL = "javascript:alert(1)"
	areas[2]["type"] = map[string]any{"type": "link", "url": input.Areas[2].URL}
	encoded, _ = json.Marshal(areas)
	if _, err := storyAreasFromAPI(string(encoded)); err == nil {
		t.Fatal("script URL accepted")
	}
}

func TestStoryRejectsReusableMediaInvalidEntitiesAndAreaOverflow(t *testing.T) {
	for _, raw := range []string{`{"type":"photo","photo":"photo:1:x"}`, `{"type":"photo","photo":"https://example.org/a.jpg"}`, `{"type":"photo","photo":"attach://missing"}`} {
		if _, err := storyContentFromAPI(raw, nil); err == nil {
			t.Fatalf("accepted %s", raw)
		}
	}
	values := map[string]string{"business_connection_id": "opaque", "active_period": "21600", "content": `{"type":"photo","photo":"attach://p"}`, "caption": "😀", "caption_entities": `[{"type":"bold","offset":1,"length":1}]`}
	if _, err := storyInputFromAPI("post", values, map[string]uploadedFile{"p": {Bytes: []byte("upload")}}); err == nil {
		t.Fatal("entity splitting a surrogate pair accepted")
	}
	area := `{"position":{"x_percentage":50,"y_percentage":50,"width_percentage":10,"height_percentage":10,"rotation_angle":0,"corner_radius_percentage":0},"type":{"type":"link","url":"https://example.org"}}`
	if _, err := storyAreasFromAPI("[" + strings.Join([]string{area, area, area, area}, ",") + "]"); err == nil {
		t.Fatal("four links accepted")
	}
}
