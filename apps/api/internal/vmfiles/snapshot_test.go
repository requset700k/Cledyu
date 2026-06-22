package vmfiles

import (
	"fmt"
	"strings"
	"testing"
)

func TestParseSnapshotAcceptsBoundedRelativeTree(t *testing.T) {
	raw := []byte(`{
		"root":"/home/lab",
		"items":[
			{"path":"work","name":"work","type":"directory","depth":1},
			{"path":"work/app.log","name":"app.log","type":"file","depth":2}
		],
		"truncated":false
	}`)

	got, err := ParseSnapshot(raw)
	if err != nil {
		t.Fatalf("ParseSnapshot() error = %v", err)
	}
	if got.Root != RootPath || len(got.Items) != 2 || got.Items[1].Path != "work/app.log" {
		t.Fatalf("ParseSnapshot() = %#v", got)
	}
}

func TestParseSnapshotRejectsUnsafeEntries(t *testing.T) {
	tests := []struct {
		name  string
		entry string
	}{
		{name: "absolute path", entry: `{"path":"/etc/passwd","name":"passwd","type":"file","depth":2}`},
		{name: "parent traversal", entry: `{"path":"work/../secret","name":"secret","type":"file","depth":3}`},
		{name: "hidden segment", entry: `{"path":".ssh/authorized_keys","name":"authorized_keys","type":"file","depth":2}`},
		{name: "excessive depth", entry: `{"path":"a/b/c/d/e","name":"e","type":"file","depth":5}`},
		{name: "unsupported type", entry: `{"path":"work/link","name":"link","type":"symlink","depth":2}`},
		{name: "mismatched name", entry: `{"path":"work/app.log","name":"other.log","type":"file","depth":2}`},
		{name: "mismatched depth", entry: `{"path":"work/app.log","name":"app.log","type":"file","depth":1}`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			raw := []byte(fmt.Sprintf(`{"root":"/home/lab","items":[%s],"truncated":false}`, tt.entry))
			if _, err := ParseSnapshot(raw); err == nil {
				t.Fatal("ParseSnapshot() error = nil, want unsafe snapshot rejection")
			}
		})
	}
}

func TestParseSnapshotRejectsWrongRootAndTooManyEntries(t *testing.T) {
	if _, err := ParseSnapshot([]byte(`{"root":"/tmp","items":[],"truncated":false}`)); err == nil {
		t.Fatal("ParseSnapshot() error = nil for non-lab root")
	}

	entries := make([]string, MaxEntries+1)
	for i := range entries {
		entries[i] = fmt.Sprintf(`{"path":"file-%03d","name":"file-%03d","type":"file","depth":1}`, i, i)
	}
	raw := []byte(`{"root":"/home/lab","items":[` + strings.Join(entries, ",") + `],"truncated":true}`)
	if _, err := ParseSnapshot(raw); err == nil {
		t.Fatal("ParseSnapshot() error = nil for oversized snapshot")
	}
}
