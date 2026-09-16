package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// A panel that answers the tools routes, checking the bearer on each, and
// records the paths it was asked for. Every tool's path and query is what
// the real routes take, so a tool whose path drifts fails here.
func fakePanel(t *testing.T, token string) (*httptest.Server, *[]string) {
	t.Helper()
	var seen []string
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "read-only", http.StatusMethodNotAllowed)
			return
		}
		if r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "who are you", http.StatusUnauthorized)
			return
		}
		seen = append(seen, r.URL.RequestURI())
		switch r.URL.Path {
		case "/api/chat/tools/sessions":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `[{"handle":1,"title":"api","state":"working"}]`)
		case "/api/chat/tools/sessions/1/messages":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `[{"kind":"user","text":"add tests"}]`)
		case "/api/chat/tools/sessions/1/screen":
			w.Header().Set("Content-Type", "text/plain")
			io.WriteString(w, "$ go test ./...\nok\n")
		case "/api/chat/tools/usage":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"days":`+r.URL.Query().Get("days")+`}`)
		case "/api/chat/tools/projects":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `[{"name":"api","sessions":1}]`)
		case "/api/chat/tools/system":
			w.Header().Set("Content-Type", "application/json")
			io.WriteString(w, `{"cores":8,"memTotalBytes":1024}`)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &seen
}

// mcpConn drives serveMCP over pipes, one request per line, one reply per
// line, the way a harness does.
type mcpConn struct {
	t     *testing.T
	in    io.WriteCloser
	lines chan string
	done  chan error
}

func dial(t *testing.T, panelURL, token string) *mcpConn {
	t.Helper()
	client, err := newMCPClient(panelURL, token)
	if err != nil {
		t.Fatal(err)
	}
	inR, inW := io.Pipe()
	outR, outW := io.Pipe()
	c := &mcpConn{t: t, in: inW, lines: make(chan string, 64), done: make(chan error, 1)}
	go func() {
		c.done <- serveMCP(inR, outW, io.Discard, client)
		outW.Close()
	}()
	// Drained by its own goroutine so a reply nobody asked for (the bug the
	// notification test looks for) shows up as a wrong line rather than as
	// a deadlock on the synchronous pipe.
	go func() {
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 0, 64*1024), mcpLineLimit)
		for sc.Scan() {
			c.lines <- sc.Text()
		}
		close(c.lines)
	}()
	t.Cleanup(func() {
		inW.Close()
		select {
		case <-c.done:
		case <-time.After(5 * time.Second):
			t.Error("serveMCP did not return when stdin closed")
		}
	})
	return c
}

// send writes one line and returns the next reply line as a response.
func (c *mcpConn) send(line string) rpcResponse {
	c.t.Helper()
	if _, err := io.WriteString(c.in, line+"\n"); err != nil {
		c.t.Fatal(err)
	}
	var text string
	select {
	case l, ok := <-c.lines:
		if !ok {
			c.t.Fatalf("the server closed stdout before answering %s", line)
		}
		text = l
	case <-time.After(10 * time.Second):
		c.t.Fatalf("no reply to %s", line)
	}
	var res rpcResponse
	if err := json.Unmarshal([]byte(text), &res); err != nil {
		c.t.Fatalf("reply to %s is not JSON: %q", line, text)
	}
	if res.JSONRPC != "2.0" {
		c.t.Errorf("reply lacks jsonrpc 2.0: %s", text)
	}
	return res
}

func (c *mcpConn) call(id int, method, params string) rpcResponse {
	c.t.Helper()
	line := `{"jsonrpc":"2.0","id":` + itoa(id) + `,"method":"` + method + `"`
	if params != "" {
		line += `,"params":` + params
	}
	return c.send(line + "}")
}

func itoa(i int) string { return strconv.Itoa(i) }

// toolText is the text content of a tools/call result.
func toolText(t *testing.T, res rpcResponse) (string, bool) {
	t.Helper()
	if res.Error != nil {
		t.Fatalf("tools/call answered a protocol error: %+v", res.Error)
	}
	b, _ := json.Marshal(res.Result)
	var r struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if err := json.Unmarshal(b, &r); err != nil || len(r.Content) != 1 || r.Content[0].Type != "text" {
		t.Fatalf("result is not one text content: %s", b)
	}
	return r.Content[0].Text, r.IsError
}

func TestMCPInitializeAndProtocolVersion(t *testing.T) {
	srv, _ := fakePanel(t, "tok")
	c := dial(t, srv.URL, "tok")
	for client, want := range map[string]string{
		"2024-11-05": "2024-11-05",
		"2025-03-26": "2025-03-26",
		"2025-06-18": "2025-06-18",
		"2099-01-01": mcpDefaultProtocol,
		"":           mcpDefaultProtocol,
	} {
		res := c.call(1, "initialize", `{"protocolVersion":"`+client+`","capabilities":{},"clientInfo":{"name":"t","version":"0"}}`)
		if res.Error != nil {
			t.Fatalf("initialize: %+v", res.Error)
		}
		b, _ := json.Marshal(res.Result)
		var r struct {
			ProtocolVersion string `json:"protocolVersion"`
			Capabilities    struct {
				Tools *struct{} `json:"tools"`
			} `json:"capabilities"`
			ServerInfo struct {
				Name    string `json:"name"`
				Version string `json:"version"`
			} `json:"serverInfo"`
		}
		if err := json.Unmarshal(b, &r); err != nil {
			t.Fatal(err)
		}
		if r.ProtocolVersion != want {
			t.Errorf("client %q: protocolVersion = %q, want %q", client, r.ProtocolVersion, want)
		}
		if r.Capabilities.Tools == nil || r.ServerInfo.Name != "vibepanel" || r.ServerInfo.Version == "" {
			t.Errorf("initialize result: %s", b)
		}
	}
	// A notification gets no reply; the ping after it must be the next line.
	if _, err := io.WriteString(c.in, `{"jsonrpc":"2.0","method":"notifications/initialized"}`+"\n"); err != nil {
		t.Fatal(err)
	}
	res := c.call(2, "ping", "")
	if res.Error != nil || string(res.ID) != "2" {
		t.Errorf("ping after the notification: id=%s err=%+v (a reply to the notification would be here)", res.ID, res.Error)
	}
}

func TestMCPToolsListAndEveryToolCall(t *testing.T) {
	srv, seen := fakePanel(t, "tok")
	c := dial(t, srv.URL, "tok")

	res := c.call(1, "tools/list", "")
	if res.Error != nil {
		t.Fatalf("tools/list: %+v", res.Error)
	}
	b, _ := json.Marshal(res.Result)
	var list struct {
		Tools []mcpTool `json:"tools"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range list.Tools {
		names = append(names, tool.Name)
		if tool.Description == "" || tool.InputSchema["type"] != "object" {
			t.Errorf("tool %s: description %q schema %v", tool.Name, tool.Description, tool.InputSchema)
		}
	}
	if got := strings.Join(names, ","); got != "list_sessions,session_messages,session_screen,usage,system,projects" {
		t.Errorf("tools = %s", got)
	}

	cases := []struct {
		name, args, wantPath, wantText string
	}{
		{"list_sessions", "{}", "/api/chat/tools/sessions", `"handle": 1`},
		{"session_messages", `{"handle":1,"n":3}`, "/api/chat/tools/sessions/1/messages?n=3", `"add tests"`},
		{"session_messages", `{"handle":1}`, "/api/chat/tools/sessions/1/messages?n=10", `"add tests"`},
		{"session_messages", `{"handle":1,"n":999}`, "/api/chat/tools/sessions/1/messages?n=50", `"add tests"`},
		{"session_screen", `{"handle":1}`, "/api/chat/tools/sessions/1/screen", "$ go test ./...\nok\n"},
		{"usage", `{"days":3}`, "/api/chat/tools/usage?days=3", `"days": 3`},
		{"usage", `{}`, "/api/chat/tools/usage?days=7", `"days": 7`},
		{"projects", "", "/api/chat/tools/projects", `"name": "api"`},
		{"system", "{}", "/api/chat/tools/system", `"cores": 8`},
	}
	for i, tc := range cases {
		*seen = nil
		params := `{"name":"` + tc.name + `"`
		if tc.args != "" {
			params += `,"arguments":` + tc.args
		}
		text, isErr := toolText(t, c.call(10+i, "tools/call", params+"}"))
		if isErr {
			t.Errorf("%s %s: isError: %s", tc.name, tc.args, text)
			continue
		}
		if len(*seen) != 1 || (*seen)[0] != tc.wantPath {
			t.Errorf("%s %s: panel saw %v, want %s", tc.name, tc.args, *seen, tc.wantPath)
		}
		if !strings.Contains(text, tc.wantText) {
			t.Errorf("%s %s: text %q lacks %q", tc.name, tc.args, text, tc.wantText)
		}
	}

	// A handle that is not a session is the panel's 404, as a tool error
	// the model can read, not a protocol error.
	text, isErr := toolText(t, c.call(30, "tools/call", `{"name":"session_screen","arguments":{"handle":9}}`))
	if !isErr || !strings.Contains(text, "404") {
		t.Errorf("unknown handle: isError=%v text=%q", isErr, text)
	}
	// A handle that is not even a positive integer never reaches the panel.
	*seen = nil
	res = c.call(31, "tools/call", `{"name":"session_screen","arguments":{"handle":0}}`)
	if res.Error == nil || res.Error.Code != rpcInvalidParams || len(*seen) != 0 {
		t.Errorf("handle 0: err=%+v seen=%v", res.Error, *seen)
	}
	res = c.call(32, "tools/call", `{"name":"write_session","arguments":{}}`)
	if res.Error == nil || res.Error.Code != rpcInvalidParams {
		t.Errorf("an unknown tool: %+v", res.Error)
	}
}

func TestMCPABadTokenIsAToolError(t *testing.T) {
	srv, _ := fakePanel(t, "right")
	c := dial(t, srv.URL, "wrong")
	text, isErr := toolText(t, c.call(1, "tools/call", `{"name":"list_sessions","arguments":{}}`))
	if !isErr || !strings.Contains(text, "401") {
		t.Errorf("isError=%v text=%q", isErr, text)
	}
}

func TestMCPUnknownMethodAndMalformedLine(t *testing.T) {
	srv, _ := fakePanel(t, "tok")
	c := dial(t, srv.URL, "tok")
	res := c.call(1, "resources/list", "")
	if res.Error == nil || res.Error.Code != rpcMethodNotFound {
		t.Errorf("unknown method: %+v", res.Error)
	}
	res = c.send(`{"jsonrpc":"2.0","id":2,"method":`)
	if res.Error == nil || res.Error.Code != rpcParseError || string(res.ID) != "null" {
		t.Errorf("malformed line: id=%s err=%+v", res.ID, res.Error)
	}
	res = c.send(`{"jsonrpc":"2.0","id":3}`)
	if res.Error == nil || res.Error.Code != rpcInvalidRequest {
		t.Errorf("no method: %+v", res.Error)
	}
	// And it is still alive.
	res = c.call(4, "ping", "")
	if res.Error != nil || string(res.ID) != "4" {
		t.Errorf("ping after the bad lines: id=%s err=%+v", res.ID, res.Error)
	}
}

func TestMCPTransportIsInsecureOnlyForLoopback(t *testing.T) {
	insecure := func(host string) bool {
		tr, ok := mcpTransport(host).(*http.Transport)
		return ok && tr.TLSClientConfig != nil && tr.TLSClientConfig.InsecureSkipVerify
	}
	for _, host := range []string{"127.0.0.1", "::1", "localhost", "127.1.2.3"} {
		if !insecure(host) {
			t.Errorf("%s: verification kept; the panel's certificate is for its public name and can never match", host)
		}
	}
	for _, host := range []string{"panel.example.com", "192.168.8.20", "10.0.0.1", "0.0.0.0", ""} {
		if insecure(host) {
			t.Errorf("%s: verification skipped for a host that is not this machine", host)
		}
	}
	// And the real thing: the fake panel's self-signed certificate is
	// accepted at 127.0.0.1, which every tools/call above relies on, so a
	// transport that verified would have failed them all with x509.
}

func TestMCPRefusesToStartWithoutItsEnvironment(t *testing.T) {
	t.Setenv("VIBEPANEL_URL", "")
	t.Setenv("VIBEPANEL_TOOLS_TOKEN", "")
	if err := cmdMCP(nil); err == nil {
		t.Error("started with neither variable")
	}
	t.Setenv("VIBEPANEL_URL", "https://127.0.0.1:18443")
	if err := cmdMCP(nil); err == nil {
		t.Error("started without a token")
	}
	t.Setenv("VIBEPANEL_URL", "")
	t.Setenv("VIBEPANEL_TOOLS_TOKEN", "tok")
	if err := cmdMCP(nil); err == nil {
		t.Error("started without a URL")
	}
	if _, err := newMCPClient("not a url", "tok"); err == nil {
		t.Error("a URL that is not one was accepted")
	}
}
