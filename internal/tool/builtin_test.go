package tool

import "testing"

func TestBuiltinToolsForPreset_BenchmarkConfiguresRestrictedBash(t *testing.T) {
	tools := BuiltinToolsForPreset(t.TempDir(), "benchmark", BashOutputFilterConfig{})
	for _, tool := range tools {
		if bash, ok := tool.(*BashTool); ok {
			if bash.Mode != "benchmark" {
				t.Fatalf("expected benchmark bash mode, got %q", bash.Mode)
			}
			return
		}
	}
	t.Fatal("expected bash tool")
}

func TestBuiltinToolsForPreset_DefaultLeavesBashUnrestricted(t *testing.T) {
	tools := BuiltinToolsForPreset(t.TempDir(), "default", BashOutputFilterConfig{})
	for _, tool := range tools {
		if bash, ok := tool.(*BashTool); ok {
			if bash.Mode != "default" {
				t.Fatalf("expected default bash mode, got %q", bash.Mode)
			}
			return
		}
	}
	t.Fatal("expected bash tool")
}
