package browse

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"
)

// The kinds the preview route answers with, and the ones the client knows,
// are one list written twice: browse.Kinds here, PreviewKind in wire.ts. A
// kind added on one side only is a file the viewer cannot show, quietly --
// the client's answer to a kind it does not know is "nothing to show".
func TestThePreviewKindsMatchTheClient(t *testing.T) {
	b, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "protocol", "wire.ts"))
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`export type PreviewKind = ([^\n]+)`).FindSubmatch(b)
	if m == nil {
		t.Fatal("no PreviewKind in wire.ts")
	}
	var client []string
	for _, q := range regexp.MustCompile(`'([a-z]+)'`).FindAllSubmatch(m[1], -1) {
		client = append(client, string(q[1]))
	}
	var server []string
	for _, k := range Kinds {
		server = append(server, string(k))
	}
	slices.Sort(client)
	slices.Sort(server)
	if !slices.Equal(client, server) {
		t.Errorf("wire.ts PreviewKind %v, browse.Kinds %v", client, server)
	}
}
