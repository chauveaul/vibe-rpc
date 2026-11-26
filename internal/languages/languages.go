package languages

import "strings"

var extToLang = map[string]string{
	".go": "Go", ".js": "JavaScript", ".mjs": "JavaScript", ".cjs": "JavaScript", ".jsx": "JavaScript",
	".ts": "TypeScript", ".tsx": "TypeScript", ".mts": "TypeScript", ".cts": "TypeScript",
	".vue": "Vue", ".svelte": "Svelte", ".html": "HTML", ".htm": "HTML",
	".css": "CSS", ".scss": "SCSS", ".sass": "Sass", ".less": "Less",
	".json": "JSON", ".jsonc": "JSON", ".py": "Python", ".pyi": "Python",
	".rb": "Ruby", ".rs": "Rust", ".java": "Java", ".kt": "Kotlin", ".kts": "Kotlin",
	".scala": "Scala", ".c": "C", ".h": "C", ".cpp": "C++", ".cc": "C++", ".hpp": "C++",
	".cs": "C#", ".php": "PHP", ".swift": "Swift", ".m": "Objective-C", ".mm": "Objective-C++",
	".zig": "Zig", ".lua": "Lua", ".dart": "Dart", ".ex": "Elixir", ".exs": "Elixir",
	".hs": "Haskell", ".ml": "OCaml", ".clj": "Clojure", ".r": "R", ".jl": "Julia",
	".sh": "Shell", ".bash": "Shell", ".zsh": "Shell", ".fish": "Shell",
	".ps1": "PowerShell", ".bat": "Batch", ".yml": "YAML", ".yaml": "YAML",
	".toml": "TOML", ".ini": "INI", ".env": "Env", ".xml": "XML",
	".md": "Markdown", ".mdx": "Markdown", ".sql": "SQL", ".graphql": "GraphQL",
	".proto": "Protobuf", ".ipynb": "Notebook", ".dockerfile": "Dockerfile",
	".gitignore": "Git", ".txt": "Text",
}

var filenameToLang = map[string]string{
	"Dockerfile":     "Dockerfile",
	"Makefile":       "Make",
	"GNUmakefile":    "Make",
	"CMakeLists.txt": "CMake",
	"go.mod":         "Go",
	"go.sum":         "Go",
	"package.json":   "JSON",
	"tsconfig.json":  "JSON",
}

func Of(path string) string {
	if path == "" {
		return ""
	}
	norm := strings.ReplaceAll(path, "\\", "/")
	base := norm
	if idx := strings.LastIndex(norm, "/"); idx != -1 {
		base = norm[idx+1:]
	}
	if lang, ok := filenameToLang[base]; ok {
		return lang
	}
	dot := strings.LastIndex(base, ".")
	if dot <= 0 {
		return ""
	}
	return extToLang[strings.ToLower(base[dot:])]
}
