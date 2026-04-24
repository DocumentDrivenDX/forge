package tool

import agent "github.com/DocumentDrivenDX/agent/internal/core"

// BuiltinToolsForPreset returns the built-in tool set used by the native
// agent harness for a prompt preset.
func BuiltinToolsForPreset(workDir, preset string, bashFilter BashOutputFilterConfig) []agent.Tool {
	tools := []agent.Tool{
		&ReadTool{WorkDir: workDir},
		&WriteTool{WorkDir: workDir},
		&EditTool{WorkDir: workDir},
		&BashTool{WorkDir: workDir, OutputFilter: bashFilter, Mode: preset},
		&FindTool{WorkDir: workDir},
		&GrepTool{WorkDir: workDir},
		&LsTool{WorkDir: workDir},
		&PatchTool{WorkDir: workDir},
	}
	if preset != "benchmark" {
		taskStore := NewTaskStore()
		tools = append(tools, &TaskTool{Store: taskStore})
	}
	return tools
}
