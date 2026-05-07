package qwen

import (
	"testing"
)

func TestToQwenName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"Read alias", "Read", "fs_open_file"},
		{"Write alias", "Write", "fs_put_file"},
		{"Edit alias", "Edit", "fs_patch_file"},
		{"Bash alias", "Bash", "shell_run"},
		{"Grep alias", "Grep", "text_search"},
		{"Glob alias", "Glob", "path_find"},
		{"NotebookEdit alias", "NotebookEdit", "notebook_patch"},
		{"WebFetch alias", "WebFetch", "http_get_url"},
		{"WebSearch alias", "WebSearch", "web_query"},
		{"Unknown tool gets u_ prefix", "TaskCreate", "u_TaskCreate"},
		{"Unknown tool gets u_ prefix", "Agent", "u_Agent"},
		{"Already obfuscated fs_* stays as-is", "fs_open_file", "fs_open_file"},
		{"Already obfuscated u_* stays as-is", "u_TaskCreate", "u_TaskCreate"},
		{"Empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := toQwenName(tt.in)
			if got != tt.want {
				t.Errorf("toQwenName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestFromQwenName(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"Reverse alias fs_open_file → Read", "fs_open_file", "Read"},
		{"Reverse alias fs_put_file → Write", "fs_put_file", "Write"},
		{"Reverse alias shell_run → Bash", "shell_run", "Bash"},
		{"Reverse alias text_search → Grep", "text_search", "Grep"},
		{"Reverse alias path_find → Glob", "path_find", "Glob"},
		{"Strip u_ prefix", "u_TaskCreate", "TaskCreate"},
		{"Strip u_ prefix multiple levels", "u_Some_Nested_Tool", "Some_Nested_Tool"},
		{"Unknown/unaltered passes through", "SomeOriginalTool", "SomeOriginalTool"},
		{"Empty string", "", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := fromQwenName(tt.in)
			if got != tt.want {
				t.Errorf("fromQwenName(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestObfuscateBareNames(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "Replace Read in instruction text",
			in:   "Use Read to view the file. Then use Edit to modify it.",
			want: "Use fs_open_file to view the file. Then use fs_patch_file to modify it.",
		},
		{
			name: "Replace Bash with shell_run",
			in:   "Call Bash to run ls -la",
			want: "Call shell_run to run ls -la",
		},
		{
			name: "Replace multiple tools at once",
			in:   "Available tools: Read, Write, Edit, Bash, Grep, Glob",
			want: "Available tools: fs_open_file, fs_put_file, fs_patch_file, shell_run, text_search, path_find",
		},
		{
			name: "Word boundaries - avoid partial matches",
			in:   "Read the file. Do not readability check.",
			want: "fs_open_file the file. Do not readability check.",
		},
		{
			name: "Empty string",
			in:   "",
			want: "",
		},
		{
			name: "No matches",
			in:   "Just regular text",
			want: "Just regular text",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := obfuscateBareNames(tt.in)
			if got != tt.want {
				t.Errorf("obfuscateBareNames(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}
