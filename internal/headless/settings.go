// Package headless is the G2 glasses' personal assistant: `claude -p` runs
// started over HTTP with an API token, streamed back as server-sent events,
// in a workspace of its own that carries a persona, a memory and skills.
//
// The contract with the glasses app is docs/headless-api.md in the
// even_vibeagent repository and the "Headless assistant" section of
// docs/api.md here. This package is the part that does not need the HTTP
// server: settings, the system prompt, the argv, the stream normaliser, the
// run registry, the transcript reader and the speech-to-text proxy.
// internal/httpapi/headless.go wires it to routes.
//
// One deliberate difference from internal/chat/assistant, stated here so
// nobody "fixes" it into agreement: that runner must never use
// bypassPermissions, because it reads what agents printed on screens. This
// one defaults to bypassPermissions, because it is the owner's own assistant,
// started by the owner's own API token, whose job is to do things -- an
// explicit decision by the person who runs the panel. The token is therefore
// full authority over the machine, and the settings page says so.
package headless

import (
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

// PermissionModes are the --permission-mode values a run may ask for. The
// order is the order the settings page and /api/headless/config offer them.
var PermissionModes = []string{"bypassPermissions", "auto", "acceptEdits", "dontAsk", "plan"}

// Model is one entry in the model picker: what --model gets, and what the
// glasses show.
type Model struct {
	ID    string `json:"id"`
	Label string `json:"label"`
}

// ASR is the speech-to-text upstream: an OpenAI-compatible
// /audio/transcriptions endpoint. The key is not here; it is sealed apart.
type ASR struct {
	BaseURL  string `json:"baseUrl"`
	Model    string `json:"model"`
	Language string `json:"language"`
	Prompt   string `json:"prompt"`
}

// Settings is what the settings page edits, as stored.
type Settings struct {
	Enabled               bool     `json:"enabled"`
	AllowedOrigins        []string `json:"allowedOrigins"`
	DefaultModel          string   `json:"defaultModel"`
	Models                []Model  `json:"models"`
	DefaultPermissionMode string   `json:"defaultPermissionMode"`
	LaunchProfileID       string   `json:"launchProfileId"`
	AssistantDir          string   `json:"assistantDir"`
	Persona               string   `json:"persona"`
	MemoryInjection       bool     `json:"memoryInjection"`
	MaxConcurrent         int      `json:"maxConcurrent"`
	TimeoutMinutes        int      `json:"timeoutMinutes"`
	ASR                   ASR      `json:"asr"`
	// AssistantProjectID is maintained by the server, never taken from a PUT:
	// it is whatever project EnsureAssistant last found or made.
	AssistantProjectID string `json:"assistantProjectId"`
}

// DefaultAssistantDir is where the assistant lives unless the owner moves it.
const DefaultAssistantDir = "~/vibeagent-assistant"

// Defaults is a fresh install's settings. Stored settings are decoded over
// this, so a field added in a later release starts at its default rather
// than at Go's zero value.
func Defaults() Settings {
	return Settings{
		Enabled:        false,
		AllowedOrigins: []string{},
		DefaultModel:   "opus",
		Models: []Model{
			{ID: "fable", Label: "Fable"},
			{ID: "opus", Label: "Opus"},
			{ID: "sonnet", Label: "Sonnet"},
			{ID: "haiku", Label: "Haiku"},
		},
		DefaultPermissionMode: "bypassPermissions",
		AssistantDir:          DefaultAssistantDir,
		MemoryInjection:       true,
		MaxConcurrent:         3,
		TimeoutMinutes:        30,
		ASR: ASR{
			BaseURL:  "https://api.siliconflow.cn/v1",
			Model:    "FunAudioLLM/SenseVoiceSmall",
			Language: "zh",
		},
	}
}

// Limits on what the settings page may store. Generous; they exist so a
// mistaken paste cannot put a megabyte into every run's argv.
const (
	maxPersona  = 16 << 10
	maxOrigins  = 32
	maxModels   = 32
	maxASRField = 1024
)

// modelID is what --model may be: an alias (opus), a full name
// (claude-opus-4-1-20250805) or a variant (opus[1m]). Nothing that starts
// with a dash, so a model can never be read as a flag.
var modelID = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:\[\]/-]{0,63}$`)

// ValidModelID reports whether id is shaped like something --model takes.
func ValidModelID(id string) bool { return modelID.MatchString(id) }

// ValidPermissionMode reports whether pm is one of PermissionModes.
func ValidPermissionMode(pm string) bool {
	for _, m := range PermissionModes {
		if m == pm {
			return true
		}
	}
	return false
}

// HasModel reports whether id is in the configured list.
func (s Settings) HasModel(id string) bool {
	for _, m := range s.Models {
		if m.ID == id {
			return true
		}
	}
	return false
}

// Normalize trims what a form leaves untrimmed and fills labels.
func (s *Settings) Normalize() {
	origins := make([]string, 0, len(s.AllowedOrigins))
	seen := map[string]bool{}
	for _, o := range s.AllowedOrigins {
		o = strings.TrimSuffix(strings.TrimSpace(o), "/")
		if o == "" || seen[o] {
			continue
		}
		seen[o] = true
		origins = append(origins, o)
	}
	s.AllowedOrigins = origins
	for i := range s.Models {
		s.Models[i].ID = strings.TrimSpace(s.Models[i].ID)
		s.Models[i].Label = strings.TrimSpace(s.Models[i].Label)
		if s.Models[i].Label == "" {
			s.Models[i].Label = s.Models[i].ID
		}
	}
	s.DefaultModel = strings.TrimSpace(s.DefaultModel)
	s.AssistantDir = strings.TrimSpace(s.AssistantDir)
	s.LaunchProfileID = strings.TrimSpace(s.LaunchProfileID)
	s.ASR.BaseURL = strings.TrimSuffix(strings.TrimSpace(s.ASR.BaseURL), "/")
	s.ASR.Model = strings.TrimSpace(s.ASR.Model)
	s.ASR.Language = strings.TrimSpace(s.ASR.Language)
}

// Validate refuses settings a run could not use. It does not look at the
// disk or the database: whether the launch profile exists and whether the
// assistant directory can be made are the caller's questions.
func (s Settings) Validate() error {
	if len(s.AllowedOrigins) > maxOrigins {
		return fmt.Errorf("at most %d allowed origins", maxOrigins)
	}
	for _, o := range s.AllowedOrigins {
		if err := validOrigin(o); err != nil {
			return err
		}
	}
	if len(s.Models) == 0 {
		return errors.New("at least one model is required")
	}
	if len(s.Models) > maxModels {
		return fmt.Errorf("at most %d models", maxModels)
	}
	ids := map[string]bool{}
	for _, m := range s.Models {
		if !ValidModelID(m.ID) {
			return fmt.Errorf("model id %q is not a model name", m.ID)
		}
		if ids[m.ID] {
			return fmt.Errorf("model %q is listed twice", m.ID)
		}
		ids[m.ID] = true
		if utf8.RuneCountInString(m.Label) > 40 {
			return fmt.Errorf("model label %q is longer than 40 characters", m.Label)
		}
	}
	if !ids[s.DefaultModel] {
		return fmt.Errorf("the default model %q is not in the model list", s.DefaultModel)
	}
	if !ValidPermissionMode(s.DefaultPermissionMode) {
		return fmt.Errorf("permission mode %q is not one of %s", s.DefaultPermissionMode, strings.Join(PermissionModes, ", "))
	}
	if s.AssistantDir == "" {
		return errors.New("the assistant directory is required")
	}
	if !strings.HasPrefix(s.AssistantDir, "/") && s.AssistantDir != "~" && !strings.HasPrefix(s.AssistantDir, "~/") {
		return errors.New("the assistant directory must be an absolute path or start with ~/")
	}
	if len(s.Persona) > maxPersona {
		return fmt.Errorf("the persona is longer than %d bytes", maxPersona)
	}
	if s.MaxConcurrent < 1 || s.MaxConcurrent > 10 {
		return errors.New("max concurrent runs must be between 1 and 10")
	}
	if s.TimeoutMinutes < 1 || s.TimeoutMinutes > 240 {
		return errors.New("the timeout must be between 1 and 240 minutes")
	}
	if s.ASR.BaseURL != "" {
		u, err := url.Parse(s.ASR.BaseURL)
		if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
			return fmt.Errorf("the speech-to-text base URL %q is not an http(s) URL", s.ASR.BaseURL)
		}
	}
	for name, v := range map[string]string{"model": s.ASR.Model, "language": s.ASR.Language, "prompt": s.ASR.Prompt} {
		if len(v) > maxASRField {
			return fmt.Errorf("the speech-to-text %s is too long", name)
		}
	}
	return nil
}

// validOrigin accepts exactly what a browser sends in Origin: scheme, host
// and an optional port, nothing after. Or the literal "null" -- a page
// opened from a file or a sandboxed frame -- which the owner has to name
// out loud. Never a wildcard: the token rides in a header, so a wildcard
// would not leak a cookie, but it would hand every page on the internet a
// way to try one.
func validOrigin(o string) error {
	if o == "null" {
		return nil
	}
	if strings.Contains(o, "*") {
		return fmt.Errorf("allowed origin %q: wildcards are not allowed", o)
	}
	u, err := url.Parse(o)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" ||
		u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.User != nil {
		return fmt.Errorf("allowed origin %q is not scheme://host[:port]", o)
	}
	return nil
}

// OriginAllowed reports whether origin is in the list, exactly.
func (s Settings) OriginAllowed(origin string) bool {
	if origin == "" {
		return false
	}
	for _, o := range s.AllowedOrigins {
		if o == origin {
			return true
		}
	}
	return false
}
