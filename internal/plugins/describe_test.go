package plugins

import (
	"strings"
	"testing"
)

// The screen is a pure function over the manifest, and this is the property
// that makes it one: every capability has its sentence in both languages, or
// the build fails. A capability added to caps.go without words is a
// permission nobody is told about.
func TestEveryCapabilityHasWordsOnTheScreen(t *testing.T) {
	for _, c := range Capabilities() {
		if strings.TrimSpace(c.Words.EN) == "" || strings.TrimSpace(c.Words.ZH) == "" {
			t.Errorf("%s has no words in both languages: %+v", c.Name, c.Words)
		}
		m := Manifest{Plugin: 1, ID: "probe", Name: Text{EN: "Probe"}, Version: "1.0.0",
			Panels:       []PanelSpec{{Slot: SlotSidePanel, Entry: "p.html", Title: Text{EN: "p"}}},
			Capabilities: []string{c.Name}}
		if err := m.Validate(); err != nil {
			t.Fatalf("%s: a manifest asking for one capability does not validate: %v", c.Name, err)
		}
		s := Describe(m, nil, "dev")
		var found *Line
		for i := range s.Lines {
			if s.Lines[i].Kind == LineCap && s.Lines[i].Code == c.Name {
				found = &s.Lines[i]
			}
		}
		if found == nil {
			t.Errorf("%s: no line on the screen", c.Name)
			continue
		}
		if found.Text.EN == "" || found.Text.ZH == "" || !found.Checkable || !found.Granted {
			t.Errorf("%s: line = %+v", c.Name, *found)
		}
		if IsDanger(c.Name) != (found.Tone == ToneRed) {
			t.Errorf("%s: danger %v but tone %s", c.Name, IsDanger(c.Name), found.Tone)
		}
	}
	if n := NetCapability("api.example.com"); n.Words.EN == "" || n.Words.ZH == "" || !KnownCapability(n.Name) {
		t.Errorf("net capability: %+v", n)
	}
	if KnownCapability("net:not a host") {
		t.Error("net: with a non-host was accepted")
	}
}

// Every rung has a line, and the confirm button escalates with the rung.
func TestEveryRungHasALineAndTheButtonEscalates(t *testing.T) {
	m, err := ParseManifest([]byte(full))
	if err != nil {
		t.Fatal(err)
	}
	s := Describe(m, nil, "dev")
	rungs := map[string]bool{}
	for _, l := range s.Lines {
		if l.Kind == LineRung {
			if l.Text.EN == "" || l.Text.ZH == "" {
				t.Errorf("rung %s has one language: %+v", l.Code, l.Text)
			}
			rungs[l.Code] = true
		}
	}
	for _, r := range []string{RungTheme, RungPanel, RungService, RungProcess, RungUnsandboxed} {
		if !rungs[r] {
			t.Errorf("no line for rung %s", r)
		}
	}
	if s.Confirm != ConfirmRun {
		t.Errorf("confirm = %s for a plugin with a process, want %s", s.Confirm, ConfirmRun)
	}

	// Theme only: Install.
	theme := Manifest{Plugin: 1, ID: "paper", Name: Text{EN: "Paper"}, Version: "1.0.0",
		Theme: &ThemeSpec{File: "t.css", Name: Text{EN: "Paper"}}}
	if got := Describe(theme, nil, "dev"); got.Confirm != ConfirmInstall {
		t.Errorf("theme confirm = %s", got.Confirm)
	}
	// A panel: Install and grant.
	pane := Manifest{Plugin: 1, ID: "pane", Name: Text{EN: "Pane"}, Version: "1.0.0",
		Panels: []PanelSpec{{Slot: SlotSidePanel, Entry: "p.html", Title: Text{EN: "p"}}}}
	if got := Describe(pane, nil, "dev"); got.Confirm != ConfirmGrant {
		t.Errorf("panel confirm = %s", got.Confirm)
	}
	// Rung 4 alone: Run this as you, and the paragraph.
	mod := Manifest{Plugin: 1, ID: "mod", Name: Text{EN: "Mod"}, Version: "1.0.0",
		Unsandboxed: &UnsandboxedSpec{Entry: "m.mjs", Tested: "1.0.x"}}
	got := Describe(mod, nil, "dev")
	if got.Confirm != ConfirmRun {
		t.Errorf("unsandboxed confirm = %s", got.Confirm)
	}
	var danger bool
	for _, l := range got.Lines {
		if l.Kind == LineDanger && l.Text == DangerParagraph {
			danger = true
		}
	}
	if !danger {
		t.Error("the rung-4 paragraph is missing")
	}
}

// The screen says what is enforced and what is a promise, in words, and the
// two are different lines.
func TestHostsAreEnforcedOrDeclaredInWords(t *testing.T) {
	m, _ := ParseManifest([]byte(full))
	s := Describe(m, nil, "dev")
	var enforced, declared int
	for _, l := range s.Lines {
		if l.Kind != LineHost {
			continue
		}
		switch l.Tone {
		case ToneEnforced:
			enforced++
			if !strings.Contains(l.Text.EN, "enforced") || !strings.Contains(l.Text.ZH, "强制") {
				t.Errorf("enforced host line does not say so: %+v", l.Text)
			}
			if !l.Checkable {
				t.Error("an enforced host is a capability the owner may untick")
			}
		case ToneAmber:
			declared++
			if !strings.Contains(l.Text.EN, "cannot check") && !strings.Contains(l.Text.EN, "internet") {
				t.Errorf("declared line does not say the panel cannot check it: %+v", l.Text)
			}
		}
	}
	if enforced != 1 || declared != 2 {
		t.Errorf("enforced %d declared %d, want 1 (the source) and 2 (the process host, the inbound route)", enforced, declared)
	}
}

// Granted decides the boxes: nil ticks everything (a first install), a list
// ticks exactly that list.
func TestGrantedDecidesTheBoxes(t *testing.T) {
	m, _ := ParseManifest([]byte(full))
	s := Describe(m, []string{"read:panel"}, "dev")
	for _, l := range s.Lines {
		if !l.Checkable {
			continue
		}
		if l.Granted != (l.Code == "read:panel") {
			t.Errorf("%s granted=%v", l.Code, l.Granted)
		}
	}
}

func TestAnOlderPanelIsRefusedByNameAndNumber(t *testing.T) {
	m, _ := ParseManifest([]byte(strings.Replace(full, `"panel": ">=1.0"`, `"panel": ">=9.0"`, 1)))
	s := Describe(m, nil, "v1.24.4")
	if s.Refused == nil {
		t.Fatal("not refused")
	}
	if !strings.Contains(s.Refused.Text.EN, ">=9.0") || !strings.Contains(s.Refused.Text.EN, "1.24.4") {
		t.Errorf("refusal names neither version: %s", s.Refused.Text.EN)
	}
	if Describe(m, nil, "dev").Refused != nil {
		t.Error("a dev build should never be refused")
	}
	if Describe(m, nil, "v9.1.0").Refused != nil {
		t.Error("a newer panel was refused")
	}
}

// The screen is drawn in the owner's language, so every line has both.
func TestEveryLineIsInBothLanguages(t *testing.T) {
	m, _ := ParseManifest([]byte(full))
	for _, l := range Describe(m, nil, "dev").Lines {
		if strings.TrimSpace(l.Text.EN) == "" || strings.TrimSpace(l.Text.ZH) == "" {
			t.Errorf("%s %s: %+v", l.Kind, l.Code, l.Text)
		}
	}
}

// A process on the panel's port is a red line on the screen that names the
// path and who may call it, in both languages, for every auth mode.
func TestTheHTTPDoorIsOnTheScreen(t *testing.T) {
	for _, mode := range ProcessHTTPAuthModes {
		raw := []byte(`{"plugin":1,"id":"door","name":{"en":"Door"},"version":"1.0.0",
		  "process":{"command":["sh","run.sh"],"http":{"auth":"` + mode + `"` +
			map[bool]string{true: `,"secret":"HOOK"`, false: ``}[mode == "hmac"] + `}}}`)
		m, err := ParseManifest(raw)
		if err != nil {
			t.Fatalf("%s: %v", mode, err)
		}
		var found *Line
		for _, l := range Describe(m, nil, "99.0.0").Lines {
			if l.Kind == LineHTTP {
				found = &l
			}
		}
		if found == nil || found.Tone != ToneRed || found.Code != mode ||
			!strings.Contains(found.Text.EN, "/api/plugin-http/door/") || !strings.Contains(found.Text.ZH, "/api/plugin-http/door/") {
			t.Errorf("%s: %+v", mode, found)
		}
		if mode == "hmac" && !contains(m.SecretNames(), "HOOK") {
			t.Error("the hmac secret is not among the names the owner is asked for")
		}
	}
	for _, bad := range []string{`"auth":"anyone"`, `"auth":"owner","secret":"X"`, `"auth":"hmac"`, `"auth":"owner","maxBody":"lots"`, `"auth":"owner","idleTimeout":"3h"`} {
		if _, err := ParseManifest([]byte(`{"plugin":1,"id":"door","name":{"en":"Door"},"version":"1.0.0","process":{"command":["sh"],"http":{` + bad + `}}}`)); err == nil {
			t.Errorf("accepted http{%s}", bad)
		}
	}
}
