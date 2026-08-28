package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

// The GitHub half of this feature, and the only thing in the panel that talks
// to a machine the operator did not name.
//
// Three rules, and all three are structural rather than remembered:
//
//   - **It runs when a person asked for it.** There is no poller, no refresh
//     timer and no prefetch. The handler that calls this is a POST for that
//     reason: a GET is something a browser will re-issue on its own.
//
//     This said "when a person presses a button" until a second caller arrived,
//     and the amendment is worth reading rather than glossing. A read-only
//     dashboard polls every two seconds forever, so a wall whose board carries
//     a pull-request tile would be forty thousand requests a day against
//     somebody's rate limit -- which is not a feature, it is an outage with a
//     nice font. What makes the second caller admissible is that all four of
//     these are true at once, and none of them is a default: an owner signed in
//     put a pull-request widget on a board, pointed that link at one project,
//     set it to disclose names, and started the panel with a token in its
//     environment. What bounds it is internal/git/warm.go: at most one request
//     per repository per GitHubTTL, shared by every viewer of every link,
//     never on the request goroutine, and stopped entirely the moment nobody is
//     looking. A ticker would have been the obvious implementation and is the
//     one thing that may not be added here -- a panel nobody is watching must
//     make no outbound request at all.
//   - **It needs a token, and there is no token in the database.** The token is
//     read from the environment the panel was started with. A settings page
//     that stores one would be a long-lived third-party credential at rest,
//     an audit surface, a rotation story and a screen explaining scopes -- for
//     a feature whose whole point is that it is optional.
//   - **Nothing degrades when it is absent.** Everything else on the git tab
//     comes off the disk. This adds one section, and without a token that
//     section says so in a line.
//
// One request, not one per pull request. The REST API needs a call per PR to
// learn whether its checks are green; the GraphQL endpoint answers the whole
// question in a single round trip, which is the difference between "a button"
// and "a button that fires thirty requests at somebody's rate limit".

// TokenEnv are the variables a token is read from, in order.
//
// The names gh(1) and every CI system already use. Deliberately not a
// VIBEPANEL_ name: config.go reports unread VIBEPANEL_* variables at startup,
// so a panel-specific name would have to be registered there as configuration,
// and this is not configuration -- it is a credential that either is in the
// environment or is not.
var TokenEnv = []string{"GITHUB_TOKEN", "GH_TOKEN"}

// Token returns the token the panel was started with, if any.
func Token() string {
	for _, key := range TokenEnv {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

// PR is one open pull request, reduced to what somebody watching six agents
// work on one repository actually needs.
//
// Branch is the field that makes this worth having at all: it is what joins a
// pull request to the local branch a session is sitting on, which is the
// question ("is my agent's branch green?") that otherwise means leaving the
// panel.
type PR struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Branch    string `json:"branch"`
	Base      string `json:"base"`
	Draft     bool   `json:"draft"`
	Author    string `json:"author"`
	URL       string `json:"url"`
	UpdatedAt int64  `json:"updatedAt"`
	// Review is GitHub's own rollup, lowercased: approved,
	// changes_requested, review_required, or empty where there is no answer.
	Review string `json:"review"`
	// Checks is the status rollup of the head commit, lowercased: success,
	// failure, pending, error, expected, or empty where nothing ran.
	Checks string `json:"checks"`
}

// Endpoint is where the query goes. A constant rather than anything derived
// from a remote URL: the remote is a string out of a working tree, and a panel
// that resolved a hostname from it would be one clone away from being told
// where to send a token.
const Endpoint = "https://api.github.com/graphql"

// requestTimeout bounds the whole exchange, connection included.
//
// Ten seconds because this is a button somebody pressed and is waiting on. The
// default http.Client has no timeout at all, which is a request goroutine held
// by whatever is on the other end of the socket.
const requestTimeout = 10 * time.Second

// maxResponse bounds what is read back. The query asks for at most prCount
// nodes, so anything larger is not an answer to it.
const maxResponse = 1 << 20

// prCount is how many open pull requests are fetched.
//
// Twenty, newest first. A repository with more open than that is one where the
// list is not the useful view anyway, and the panel says how many there are.
const prCount = 20

// Client queries GitHub.
//
// Endpoint and HTTP are fields so a test can point the whole thing at an
// httptest server. There is no other way to test this: the alternative is a
// suite that talks to github.com, which needs a credential, a network and
// somebody else's rate limit to pass.
type Client struct {
	Token    string
	Endpoint string
	HTTP     *http.Client
}

// query is the whole conversation with GitHub.
//
// Owner and name go in as *variables*, never interpolated. They come out of a
// file in a working tree; ParseRemote already restricts them to a character
// class, and this makes that the second line of defence rather than the only
// one.
const query = `query($owner:String!,$name:String!,$n:Int!){
  repository(owner:$owner,name:$name){
    pullRequests(states:OPEN,first:$n,orderBy:{field:UPDATED_AT,direction:DESC}){
      totalCount
      nodes{
        number title url isDraft updatedAt headRefName baseRefName reviewDecision
        author{login}
        commits(last:1){nodes{commit{statusCheckRollup{state}}}}
      }
    }
  }
}`

// Result is what one press of the button produced.
type Result struct {
	// Total is how many open pull requests there are, which is not len(PRs)
	// once prCount bites.
	Total int  `json:"total"`
	PRs   []PR `json:"prs"`
}

// OpenPRs asks GitHub for the open pull requests on one repository.
func (c Client) OpenPRs(ctx context.Context, owner, name string) (Result, error) {
	if c.Token == "" {
		return Result{}, fmt.Errorf("github: no token")
	}
	if !validSegment(owner) || !validSegment(name) {
		return Result{}, fmt.Errorf("github: unusable repository name")
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = Endpoint
	}
	body, err := json.Marshal(map[string]any{
		"query":     query,
		"variables": map[string]any{"owner": owner, "name": name, "n": prCount},
	})
	if err != nil {
		return Result{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	req.Header.Set("Authorization", "Bearer "+c.Token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "vibepanel")

	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: requestTimeout}
	}
	res, err := hc.Do(req)
	if err != nil {
		return Result{}, fmt.Errorf("github: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return Result{}, fmt.Errorf("github: %w", err)
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		// Named apart from every other failure because it is the one with an
		// action attached: the token is wrong, expired, or lacks `repo` on a
		// private repository.
		return Result{}, fmt.Errorf("github: the token was refused (%d)", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		return Result{}, fmt.Errorf("github: %s", res.Status)
	}

	var parsed struct {
		Data struct {
			Repository *struct {
				PullRequests struct {
					TotalCount int `json:"totalCount"`
					Nodes      []struct {
						Number      int    `json:"number"`
						Title       string `json:"title"`
						URL         string `json:"url"`
						IsDraft     bool   `json:"isDraft"`
						UpdatedAt   string `json:"updatedAt"`
						HeadRefName string `json:"headRefName"`
						BaseRefName string `json:"baseRefName"`
						// Null for "no review has been asked for", which is
						// different from "review required".
						ReviewDecision *string `json:"reviewDecision"`
						Author         *struct {
							Login string `json:"login"`
						} `json:"author"`
						Commits struct {
							Nodes []struct {
								Commit struct {
									StatusCheckRollup *struct {
										State string `json:"state"`
									} `json:"statusCheckRollup"`
								} `json:"commit"`
							} `json:"nodes"`
						} `json:"commits"`
					} `json:"nodes"`
				} `json:"pullRequests"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return Result{}, fmt.Errorf("github: unreadable answer")
	}
	// GraphQL answers 200 with an errors array, so a handler that only checks
	// the status code reports success and an empty list. A repository that has
	// been renamed, or that the token cannot see, arrives exactly this way.
	if len(parsed.Errors) > 0 {
		msg := parsed.Errors[0].Message
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return Result{}, fmt.Errorf("github: %s", msg)
	}
	if parsed.Data.Repository == nil {
		return Result{}, fmt.Errorf("github: no such repository")
	}

	out := Result{Total: parsed.Data.Repository.PullRequests.TotalCount, PRs: []PR{}}
	for _, n := range parsed.Data.Repository.PullRequests.Nodes {
		pr := PR{
			Number: n.Number,
			Title:  n.Title,
			Branch: n.HeadRefName,
			Base:   n.BaseRefName,
			Draft:  n.IsDraft,
			URL:    n.URL,
		}
		if n.Author != nil {
			pr.Author = n.Author.Login
		}
		if n.ReviewDecision != nil {
			pr.Review = strings.ToLower(*n.ReviewDecision)
		}
		if len(n.Commits.Nodes) > 0 {
			if roll := n.Commits.Nodes[0].Commit.StatusCheckRollup; roll != nil {
				pr.Checks = strings.ToLower(roll.State)
			}
		}
		if ts, err := time.Parse(time.RFC3339, n.UpdatedAt); err == nil {
			pr.UpdatedAt = ts.Unix()
		}
		out.PRs = append(out.PRs, pr)
	}
	return out, nil
}
