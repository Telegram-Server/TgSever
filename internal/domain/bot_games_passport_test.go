package domain

import "testing"

func TestGameHighScoresIncludeTopThreeAndUserNeighbors(t *testing.T) {
	scores := make([]GameScore, 12)
	for i := range scores {
		scores[i] = GameScore{UserID: int64(i + 1), Score: 120 - i*10}
	}
	for _, tc := range []struct {
		userID    int64
		positions []int
	}{{9, []int{1, 2, 3, 7, 8, 9, 10, 11}}, {1, []int{1, 2, 3}}, {99, []int{1, 2, 3}}} {
		got := GameHighScoreWindow(scores, tc.userID)
		if len(got) != len(tc.positions) {
			t.Fatalf("user %d: %+v", tc.userID, got)
		}
		for i, p := range tc.positions {
			if got[i].Position != p || got[i].UserID != int64(p) || got[i].Score != 120-(p-1)*10 {
				t.Fatalf("user %d row %d: %+v", tc.userID, i, got[i])
			}
		}
	}
}

func TestPassportRejectedFilesCannotBeFixedByReordering(t *testing.T) {
	old := PassportElement{Type: "utility_bill", Files: []PassportFile{{Hash: "scan-a"}, {Hash: "scan-b"}}}
	reordered := PassportElement{Type: old.Type, Files: []PassportFile{old.Files[1], old.Files[0]}}
	rejection := PassportError{Source: "files", Type: old.Type, FileHashes: []string{"scan-a", "scan-b"}}
	if !PassportErrorUnchanged(old, reordered, rejection) {
		t.Fatal("reordering uncorrected scans bypassed the rejection")
	}
	corrected := PassportElement{Type: old.Type, Files: []PassportFile{{Hash: "new-scan"}, old.Files[1]}}
	if PassportErrorUnchanged(old, corrected, rejection) {
		t.Fatal("replacing the rejected scan did not resolve the rejection")
	}
}
