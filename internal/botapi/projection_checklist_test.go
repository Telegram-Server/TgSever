package botapi

import (
	"testing"

	"telesrv/internal/domain"
)

func TestEditedChecklistProjection(t *testing.T) {
	item, kind, ok := apiUpdate(domain.UpdateEvent{Type: domain.UpdateEventEditMessage, Pts: 12, Message: domain.Message{
		ID: 4, OwnerUserID: 1001, Out: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: 2001}, From: domain.Peer{Type: domain.PeerTypeUser, ID: 1001}, Date: 1700000000, EditDate: 1700000001,
		Media: &domain.MessageMedia{Kind: domain.MessageMediaKindTodo, Todo: &domain.MessageTodo{Title: "shopping", OthersCanAppend: true, OthersCanComplete: true, Items: []domain.MessageTodoItem{{ID: 7, Title: "milk", Entities: []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 4}}}}, Completions: []domain.MessageTodoCompletion{{ID: 7, CompletedBy: 2001, Date: 1700000001}}}},
	}})
	if !ok || kind != "edited_message" {
		t.Fatalf("update = %#v, %s, %v", item, kind, ok)
	}
	msg := item["edited_message"].(map[string]any)
	checklist, ok := msg["checklist"].(map[string]any)
	if !ok || checklist["title"] != "shopping" || checklist["others_can_add_tasks"] != true || checklist["others_can_mark_tasks_as_done"] != true {
		t.Fatalf("checklist = %#v", msg["checklist"])
	}
	tasks := checklist["tasks"].([]map[string]any)
	if len(tasks) != 1 || tasks[0]["id"] != 7 || tasks[0]["text"] != "milk" || tasks[0]["completion_date"] != 1700000001 {
		t.Fatalf("tasks = %#v", tasks)
	}
	if user := tasks[0]["completed_by_user"].(map[string]any); user["id"] != int64(2001) {
		t.Fatalf("completed by = %#v", user)
	}
	if entities := tasks[0]["text_entities"].([]map[string]any); len(entities) != 1 || entities[0]["type"] != "bold" {
		t.Fatalf("entities = %#v", entities)
	}
}
