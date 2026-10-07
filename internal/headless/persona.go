package headless

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// DefaultPersona is the system prompt when the settings page leaves it
// empty. Word for word the contract's; {assistantDir} becomes the absolute
// path.
const DefaultPersona = `你是用户的私人助理，运行在用户家里的服务器上，通过 Even G2 智能眼镜被语音调用。
你的工作是帮用户办事和查信息：搜索与核实资料、整理信息、计算与换算、写和运行脚本、
处理文件、调用已配置的工具。除非用户明确要求，不要主动修改代码仓库。

表达：
- 输入来自语音识别，可能有同音字或断句错误，按意图理解；关键操作前如果有歧义先确认。
- 回复显示在 576×288 的单色小屏上：第一句给结论，全文尽量 300 字以内；
  不用表格、emoji 和复杂 Markdown，列表用短句；需要用户选择时给编号选项。
- 引用网上信息时简要标注来源站点。

记忆（参考 Hermes）：
- 用户档案：{assistantDir}/memories/USER.md；长期记忆：{assistantDir}/memories/MEMORY.md。
- 学到值得长期记住的信息（偏好、常用地点、账号习惯、环境事实、踩过的坑）时，
  直接编辑这两个文件；保持精炼，每个文件不超过 3000 字，过时内容及时删改。
- 不要把密码、密钥等敏感信息写入记忆。

技能：
- 可复用的做法沉淀为技能：{assistantDir}/.claude/skills/<名字>/SKILL.md
  （带 name/description 前置信息）。完成一个以后可能重复的任务后，主动创建或改进技能。

工作区：{assistantDir} 是你的家目录，临时文件和笔记放在 {assistantDir}/notes/。`

// The assistant directory's starting contents. Written only where nothing
// is: the files are the assistant's to edit from the first run on, and a
// panel upgrade that put them back would erase what it learned.
const (
	templateUser   = "# 用户档案\n\n（助理会在这里记录你的偏好）\n"
	templateMemory = "# 长期记忆\n"
	templateClaude = `# 私人助理工作区

这是 G2 眼镜私人助理的家目录。用户档案在 memories/USER.md，长期记忆在
memories/MEMORY.md：学到值得长期记住的事情时直接更新它们（每个文件不超过 3000 字，
不写密码和密钥）。可复用的做法写成技能，放在 .claude/skills/<名字>/SKILL.md；
临时文件和笔记放在 notes/。
`
)

// memoryCap bounds each memory file in the snapshot. The persona asks the
// assistant to keep each under 3,000 characters; this is the backstop for
// when it has not, so one runaway file cannot fill the argv of every run.
const memoryCap = 6000

// ExpandDir resolves "~" and "~/..." against home and cleans the result.
func ExpandDir(dir, home string) (string, error) {
	switch {
	case dir == "~":
		dir = home
	case strings.HasPrefix(dir, "~/"):
		dir = filepath.Join(home, dir[2:])
	}
	if home == "" && strings.HasPrefix(dir, "~") {
		return "", errors.New("no home directory to resolve ~ against")
	}
	if !filepath.IsAbs(dir) {
		return "", fmt.Errorf("%q is not an absolute path", dir)
	}
	return filepath.Clean(dir), nil
}

// EnsureAssistantDir creates the workspace and its starting files, leaving
// every file that already exists exactly as it is.
func EnsureAssistantDir(dir string) error {
	for _, d := range []string{dir, filepath.Join(dir, "memories"), filepath.Join(dir, ".claude", "skills"), filepath.Join(dir, "notes")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("creating %s: %w", d, err)
		}
	}
	files := map[string]string{
		filepath.Join(dir, "CLAUDE.md"):             templateClaude,
		filepath.Join(dir, "memories", "USER.md"):   templateUser,
		filepath.Join(dir, "memories", "MEMORY.md"): templateMemory,
	}
	for path, body := range files {
		// O_EXCL: the check and the write are one step, so a file the
		// assistant wrote a moment ago is never replaced.
		f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if errors.Is(err, fs.ErrExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("creating %s: %w", path, err)
		}
		_, werr := f.WriteString(body)
		cerr := f.Close()
		if werr != nil {
			return werr
		}
		if cerr != nil {
			return cerr
		}
	}
	return nil
}

// PromptInput is everything SystemPrompt needs.
type PromptInput struct {
	// Persona is the settings page's text; empty means DefaultPersona.
	Persona string
	// AssistantDir is the absolute workspace path.
	AssistantDir string
	// Memory says whether to append the memory snapshot.
	Memory      bool
	ProjectName string
	ProjectPath string
	Host        string
	// Context is what the client says about itself ("设备：Even G2 眼镜").
	Context string
}

// SystemPrompt is the --append-system-prompt text: persona, the memory
// snapshot, where the run is, and the client's context.
func SystemPrompt(in PromptInput) string {
	persona := strings.TrimSpace(in.Persona)
	if persona == "" {
		persona = DefaultPersona
	}
	persona = strings.ReplaceAll(persona, "{assistantDir}", in.AssistantDir)
	parts := []string{persona}
	if in.Memory {
		parts = append(parts, MemorySnapshot(in.AssistantDir))
	}
	parts = append(parts, fmt.Sprintf("当前工作目录：%s (%s)；服务器：%s", in.ProjectName, in.ProjectPath, in.Host))
	if c := strings.TrimSpace(in.Context); c != "" {
		parts = append(parts, c)
	}
	return strings.Join(parts, "\n\n")
}

// MemorySnapshot is the two memory files as they are now. A missing file is
// shown as empty rather than left out, so the assistant knows where to write.
func MemorySnapshot(dir string) string {
	var b strings.Builder
	b.WriteString("## 记忆快照（会话开始时的内容）")
	for _, name := range []string{"USER.md", "MEMORY.md"} {
		b.WriteString("\n### " + name + "\n")
		body, err := os.ReadFile(filepath.Join(dir, "memories", name))
		if err != nil {
			b.WriteString("（空）")
			continue
		}
		b.WriteString(capText(strings.TrimSpace(string(body)), memoryCap))
	}
	return b.String()
}

// capText cuts s to n runes, saying so.
func capText(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	r := []rune(s)
	return string(r[:n]) + "\n（已截断）"
}
