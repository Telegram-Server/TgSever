package main

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// decodeNotes 把 {"语言":"说明"} 形式的 JSON 合并进 notes；只接受一个 JSON 对象。
func decodeNotes(data []byte, notes map[string]string) error {
	var parsed map[string]string
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&parsed); err != nil {
		return fmt.Errorf("decode release notes: %w", err)
	}
	if decoder.More() {
		return fmt.Errorf("release notes must be a single JSON object")
	}
	for lang, text := range parsed {
		notes[lang] = text
	}
	return nil
}
