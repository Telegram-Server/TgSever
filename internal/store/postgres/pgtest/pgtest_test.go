package pgtest

import "testing"

func TestCloneStrategy(t *testing.T) {
	for _, tt := range []struct {
		env     string
		private bool
		want    string
		wantErr bool
	}{
		{"", false, "", false},
		{"", true, " STRATEGY FILE_COPY", false},
		{"file_copy", false, " STRATEGY FILE_COPY", false},
		{" WAL_LOG ", true, " STRATEGY WAL_LOG", false},
		{"copy; DROP DATABASE x", false, "", true},
	} {
		t.Setenv(EnvCloneStrategy, tt.env)
		got, err := cloneStrategy(tt.private)
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("%s=%q private=%v: got %q err %v, want %q err %v", EnvCloneStrategy, tt.env, tt.private, got, err, tt.want, tt.wantErr)
		}
	}
}
