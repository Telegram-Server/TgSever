package pgtest

import "testing"

func TestCloneStrategy(t *testing.T) {
	for _, tt := range []struct {
		env, want string
		wantErr   bool
	}{
		{"", "", false},
		{"file_copy", " STRATEGY FILE_COPY", false},
		{" WAL_LOG ", " STRATEGY WAL_LOG", false},
		{"copy; DROP DATABASE x", "", true},
	} {
		t.Setenv(EnvCloneStrategy, tt.env)
		got, err := cloneStrategy()
		if (err != nil) != tt.wantErr || got != tt.want {
			t.Errorf("%s=%q: got %q err %v, want %q err %v", EnvCloneStrategy, tt.env, got, err, tt.want, tt.wantErr)
		}
	}
}
