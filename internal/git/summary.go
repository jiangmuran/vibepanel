package git

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The pull-request question, asked in counts.
//
// A separate query from OpenPRs and a separate parse, deliberately, and the
// reason is the disclosure rather than the shape. OpenPRs answers the git tab,
// which is behind a password and shows a signed-in owner the titles, the
// numbers, the authors and the branches. This answers a *wall*, and what may
// leave the machine for a wall is how many, how many green, how many red — no
// title, no number, no author, no branch, no URL. Reducing OpenPRs' result to
// counts at the call site would put the whole disclosure decision in a loop
// somebody edits later; making it a second query means the fields a wall can
// know are the fields this file asks GitHub for, and adding one is a diff here.
//
// One round trip for both halves. `open` and `merged` are two aliases of the
// same connection in the same query, so "how many are open, how many merged
// today" costs what "how many are open" cost.

// mergedCount is how many recently-merged pull requests are examined.
//
// Ordered by merge time, so the first twenty are the twenty most recent merges
// and "how many today" is exact for any repository merging fewer than twenty a
// day. Past that the count is a floor and PRSummary.MergedPartial says so —
// which is the same promise Status.ChangesTruncated makes about a file list.
const mergedCount = 20

// summaryQuery asks for counts and rollups and nothing that is a name.
//
// Owner and name are variables, never interpolated, for the reason `query`
// gives one file over: they come out of a file in a working tree.
const summaryQuery = `query($owner:String!,$name:String!,$n:Int!,$m:Int!){
  repository(owner:$owner,name:$name){
    open: pullRequests(states:OPEN,first:$n,orderBy:{field:UPDATED_AT,direction:DESC}){
      totalCount
      nodes{
        isDraft reviewDecision
        commits(last:1){nodes{commit{statusCheckRollup{state}}}}
      }
    }
    merged: pullRequests(states:MERGED,first:$m,orderBy:{field:UPDATED_AT,direction:DESC}){
      nodes{ mergedAt }
    }
  }
}`

// Summarise counts a repository's pull requests.
//
// dayStart is the start of the server's local day, in unix seconds. Passed in
// rather than computed here so that "merged today" is the same day the rest of
// the dashboard means by today — a package deciding for itself would put the
// commit count and the merge count on different days for a panel in a timezone
// nobody thought about.
func (c Client) Summarise(ctx context.Context, owner, name string, dayStart int64) (PRSummary, error) {
	if c.Token == "" {
		return PRSummary{}, fmt.Errorf("github: no token")
	}
	if !validSegment(owner) || !validSegment(name) {
		return PRSummary{}, fmt.Errorf("github: unusable repository name")
	}
	endpoint := c.Endpoint
	if endpoint == "" {
		endpoint = Endpoint
	}
	body, err := json.Marshal(map[string]any{
		"query": summaryQuery,
		"variables": map[string]any{
			"owner": owner, "name": name, "n": prCount, "m": mergedCount,
		},
	})
	if err != nil {
		return PRSummary{}, err
	}

	ctx, cancel := context.WithTimeout(ctx, requestTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(body))
	if err != nil {
		return PRSummary{}, err
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
		return PRSummary{}, fmt.Errorf("github: %w", err)
	}
	defer res.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(res.Body, maxResponse))
	if err != nil {
		return PRSummary{}, fmt.Errorf("github: %w", err)
	}
	if res.StatusCode == http.StatusUnauthorized || res.StatusCode == http.StatusForbidden {
		return PRSummary{}, fmt.Errorf("github: the token was refused (%d)", res.StatusCode)
	}
	if res.StatusCode != http.StatusOK {
		return PRSummary{}, fmt.Errorf("github: %s", res.Status)
	}

	var parsed struct {
		Data struct {
			Repository *struct {
				Open struct {
					TotalCount int `json:"totalCount"`
					Nodes      []struct {
						IsDraft        bool    `json:"isDraft"`
						ReviewDecision *string `json:"reviewDecision"`
						Commits        struct {
							Nodes []struct {
								Commit struct {
									StatusCheckRollup *struct {
										State string `json:"state"`
									} `json:"statusCheckRollup"`
								} `json:"commit"`
							} `json:"nodes"`
						} `json:"commits"`
					} `json:"nodes"`
				} `json:"open"`
				Merged struct {
					Nodes []struct {
						MergedAt string `json:"mergedAt"`
					} `json:"nodes"`
				} `json:"merged"`
			} `json:"repository"`
		} `json:"data"`
		Errors []struct {
			Message string `json:"message"`
		} `json:"errors"`
	}
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return PRSummary{}, fmt.Errorf("github: unreadable answer")
	}
	// GraphQL answers 200 with an errors array, so a caller that only checks the
	// status code reports success and every count as zero -- which on a wall is
	// "nothing is open" rather than "we could not ask".
	if len(parsed.Errors) > 0 {
		msg := parsed.Errors[0].Message
		if len(msg) > 200 {
			msg = msg[:200]
		}
		return PRSummary{}, fmt.Errorf("github: %s", msg)
	}
	if parsed.Data.Repository == nil {
		return PRSummary{}, fmt.Errorf("github: no such repository")
	}

	repo := parsed.Data.Repository
	out := PRSummary{Open: repo.Open.TotalCount}
	for _, n := range repo.Open.Nodes {
		if n.IsDraft {
			out.Draft++
		}
		if n.ReviewDecision != nil {
			switch strings.ToLower(*n.ReviewDecision) {
			case "approved":
				out.Approved++
			case "changes_requested":
				out.ChangesRequested++
			}
		}
		if len(n.Commits.Nodes) == 0 {
			continue
		}
		roll := n.Commits.Nodes[0].Commit.StatusCheckRollup
		if roll == nil {
			// No checks ran at all, which is a third answer and not a green
			// one. Counted in none of the three on purpose: a repository with
			// no CI must not read as a repository whose CI is passing.
			continue
		}
		switch strings.ToUpper(roll.State) {
		case "SUCCESS":
			out.Green++
		case "FAILURE", "ERROR":
			out.Red++
		case "PENDING", "EXPECTED":
			out.Pending++
		}
	}
	for _, n := range repo.Merged.Nodes {
		ts, err := time.Parse(time.RFC3339, n.MergedAt)
		if err != nil {
			continue
		}
		if ts.Unix() >= dayStart {
			out.MergedToday++
		}
	}
	// Every one of the newest merges was today, so there may be more further
	// down the list than this query asked for.
	out.MergedPartial = out.MergedToday >= len(repo.Merged.Nodes) && out.MergedToday > 0
	return out, nil
}
