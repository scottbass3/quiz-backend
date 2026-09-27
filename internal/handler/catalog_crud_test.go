package handler

import (
	"encoding/json"
	"net/http"
	"testing"

	"github.com/scottbass3/quizz-backend/internal/store"
)

// listWithQuestions creates a public list with n questions (as admin) and
// returns the list ID and the question IDs in order.
func listWithQuestions(t *testing.T, do func(a actor, method, path, body string, out any) int, n int) (string, []string) {
	t.Helper()
	var list store.QuestionListRecord
	do(admin, "POST", "/question-lists", `{"name":"L","visibility":"public"}`, &list)
	ids := make([]string, n)
	for i := range ids {
		var created map[string]string
		do(admin, "POST", "/question-lists/"+list.ID+"/questions", questionBody(""), &created)
		ids[i] = created["question_id"]
	}
	return list.ID, ids
}

func questionIDs(t *testing.T, do func(a actor, method, path, body string, out any) int, listID string) []string {
	t.Helper()
	var qs []store.QuestionRecord
	do(admin, "GET", "/question-lists/"+listID+"/questions", "", &qs)
	ids := make([]string, len(qs))
	for i, q := range qs {
		if q.OrderIndex != i {
			t.Fatalf("order_index should be contiguous, got %d at position %d", q.OrderIndex, i)
		}
		ids[i] = q.ID
	}
	return ids
}

func TestUpdateAndDeleteList(t *testing.T) {
	m := newMemStore()
	srv := catalogServer(t, m)
	do := func(a actor, method, path, body string, out any) int { return call(t, srv, a, method, path, body, out) }
	listID, _ := listWithQuestions(t, do, 2)
	do(admin, "POST", "/question-lists/"+listID+"/themes", `{"name":"Local"}`, nil)

	var updated store.QuestionListRecord
	if code := do(admin, "PUT", "/question-lists/"+listID, `{"name":" Renamed ","description":"new"}`, &updated); code != http.StatusOK {
		t.Fatalf("update: expected 200, got %d", code)
	}
	if updated.Name != "Renamed" || updated.Description != "new" || updated.Visibility != "public" {
		t.Fatalf("unexpected updated list %+v", updated)
	}
	if code := do(admin, "PUT", "/question-lists/"+listID, `{"name":""}`, nil); code != http.StatusBadRequest {
		t.Fatalf("empty name: expected 400, got %d", code)
	}
	if code := do(alice, "PUT", "/question-lists/"+listID, `{"name":"x"}`, nil); code != http.StatusForbidden {
		t.Fatalf("user on public list: expected 403, got %d", code)
	}
	if code := do(alice, "DELETE", "/question-lists/"+listID, "", nil); code != http.StatusForbidden {
		t.Fatalf("user delete: expected 403, got %d", code)
	}

	if code := do(admin, "DELETE", "/question-lists/"+listID, "", nil); code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", code)
	}
	if len(m.questions) != 0 || len(m.themes) != 0 {
		t.Fatalf("questions and themes should go with the list, left %d / %d", len(m.questions), len(m.themes))
	}
	if code := do(admin, "GET", "/question-lists/"+listID+"/questions", "", nil); code != http.StatusNotFound {
		t.Fatalf("deleted list: expected 404, got %d", code)
	}
}

func TestDeleteQuestion(t *testing.T) {
	srv := catalogServer(t, newMemStore())
	do := func(a actor, method, path, body string, out any) int { return call(t, srv, a, method, path, body, out) }
	listID, ids := listWithQuestions(t, do, 3)
	otherList, _ := listWithQuestions(t, do, 1)

	if code := do(admin, "DELETE", "/question-lists/"+otherList+"/questions/"+ids[1], "", nil); code != http.StatusNotFound {
		t.Fatalf("question of another list: expected 404, got %d", code)
	}
	if code := do(alice, "DELETE", "/question-lists/"+listID+"/questions/"+ids[1], "", nil); code != http.StatusForbidden {
		t.Fatalf("non-editor: expected 403, got %d", code)
	}
	if code := do(admin, "DELETE", "/question-lists/"+listID+"/questions/"+ids[1], "", nil); code != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", code)
	}
	if got := questionIDs(t, do, listID); len(got) != 2 || got[0] != ids[0] || got[1] != ids[2] {
		t.Fatalf("expected [%s %s], got %v", ids[0], ids[2], got)
	}
}

func TestReorderQuestions(t *testing.T) {
	srv := catalogServer(t, newMemStore())
	do := func(a actor, method, path, body string, out any) int { return call(t, srv, a, method, path, body, out) }
	listID, ids := listWithQuestions(t, do, 3)
	path := "/question-lists/" + listID + "/questions/order"
	order := func(ids ...string) string {
		b, _ := json.Marshal(map[string][]string{"question_ids": ids})
		return string(b)
	}

	var reordered []store.QuestionRecord
	if code := do(admin, "PUT", path, order(ids[2], ids[0], ids[1]), &reordered); code != http.StatusOK {
		t.Fatalf("reorder: expected 200, got %d", code)
	}
	if got := questionIDs(t, do, listID); got[0] != ids[2] || got[1] != ids[0] || got[2] != ids[1] {
		t.Fatalf("unexpected order %v", got)
	}
	if len(reordered) != 3 || reordered[0].ID != ids[2] {
		t.Fatalf("response should list the reordered questions, got %+v", reordered)
	}

	invalid := map[string]string{
		"missing one": order(ids[0], ids[1]),
		"duplicate":   order(ids[0], ids[0], ids[1]),
		"unknown id":  order(ids[0], ids[1], "nope"),
	}
	for name, body := range invalid {
		var errBody map[string]string
		if code := do(admin, "PUT", path, body, &errBody); code != http.StatusBadRequest || errBody["code"] != "invalid_order" {
			t.Errorf("%s: expected 400 invalid_order, got %d %v", name, code, errBody)
		}
	}
	if code := do(alice, "PUT", path, order(ids...), nil); code != http.StatusForbidden {
		t.Fatalf("non-editor: expected 403, got %d", code)
	}
}
