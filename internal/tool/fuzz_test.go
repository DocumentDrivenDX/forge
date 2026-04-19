package tool

import (
	"context"
	"testing"
)

func FuzzReadParams(f *testing.F) {
	// Seed corpus
	f.Add([]byte(`{"path":"main.go"}`))
	f.Add([]byte(`{"path":"main.go","offset":5,"limit":10}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"path":""}`))
	f.Add([]byte(`{"path":"/dev/null","offset":-1}`))
	f.Add([]byte(`not json`))
	f.Add([]byte(`{"path":"../../../etc/passwd"}`))
	f.Add([]byte{})

	tool := &ReadTool{WorkDir: f.TempDir()}
	f.Fuzz(func(t *testing.T, data []byte) {
		// Must not panic
		_, _ = tool.Execute(context.Background(), data)
	})
}

func FuzzEditParams(f *testing.F) {
	f.Add([]byte(`{"path":"test.txt","old_string":"foo","new_string":"bar"}`))
	f.Add([]byte(`{"path":"test.txt","old_string":"","new_string":"bar"}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`not json`))
	f.Add([]byte{})

	tool := &EditTool{WorkDir: f.TempDir()}
	f.Fuzz(func(t *testing.T, data []byte) {
		_, _ = tool.Execute(context.Background(), data)
	})
}

func FuzzBashParams(f *testing.F) {
	f.Add([]byte(`{"command":"echo hello"}`))
	f.Add([]byte(`{"command":"echo hello","timeout_ms":100}`))
	f.Add([]byte(`{}`))
	f.Add([]byte(`{"command":""}`))
	f.Add([]byte(`not json`))
	f.Add([]byte{})

	tool := &BashTool{WorkDir: f.TempDir()}
	f.Fuzz(func(t *testing.T, data []byte) {
		ctx, cancel := context.WithTimeout(context.Background(), 50*1e6) // 50ms
		defer cancel()
		_, _ = tool.Execute(ctx, data)
	})
}
