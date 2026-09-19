package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

// Scope is an already-authorized repository and PR selection. RepositoryID and
// PullRequest are required. Owner and Repository are optional display context;
// neither is used for token grants, API paths, or identity comparisons.
// Callers map their application scope here after applying current tool authority.
type Scope struct {
	RepositoryID int64  `json:"repository_id"`
	PullRequest  int    `json:"pull_request"`
	Owner        string `json:"owner,omitempty"`
	Repository   string `json:"repository,omitempty"`
}

type preparedPull struct {
	repository Repository
	number     int
	token      string
	metadata   PullRequest
}

// preparePull resolves the current name using the ID-restricted token, then
// validates PR identity before any further read or mutation. It deliberately
// does not cache names: a token may survive a repository rename.
// ctx must carry the public operation's deadline, shared by all these requests.
func (c *Client) preparePull(ctx context.Context, scope Scope, write bool) (preparedPull, error) {
	if scope.RepositoryID <= 0 || scope.PullRequest <= 0 {
		return preparedPull{}, errors.New("github scope requires positive repository ID and pull request number")
	}
	token, err := c.installationToken(ctx, scope.RepositoryID, write)
	if err != nil {
		return preparedPull{}, err
	}
	var listing struct {
		TotalCount   *int         `json:"total_count"`
		Repositories []Repository `json:"repositories"`
	}
	header, err := c.request(ctx, token, http.MethodGet,
		"/installation/repositories?per_page=100&page=1", nil, &listing)
	if err != nil {
		return preparedPull{}, err
	}
	// A token restricted to exactly one ID must resolve exactly that repository.
	// Never scan an installation indefinitely or fall back to a stored name.
	if listing.TotalCount == nil || *listing.TotalCount < 0 || listing.Repositories == nil ||
		len(listing.Repositories) > 100 {
		return preparedPull{}, &APIError{Code: InvalidResponse}
	}
	next, err := c.nextPage(header.Values("Link"), "/installation/repositories", PageOptions{Page: 1, PerPage: 100})
	if err != nil {
		return preparedPull{}, err
	}
	if next != 0 || *listing.TotalCount != 1 || len(listing.Repositories) != 1 ||
		listing.Repositories[0].ID != scope.RepositoryID {
		return preparedPull{}, &APIError{Code: ScopeMismatch}
	}
	repository := listing.Repositories[0]
	if !validRepositorySegment(repository.Owner.Login) || !validRepositorySegment(repository.Name) {
		return preparedPull{}, &APIError{Code: InvalidResponse}
	}
	pull := preparedPull{repository: repository, number: scope.PullRequest, token: token}
	_, err = c.request(ctx, token, http.MethodGet, pullPath(repository, scope.PullRequest), nil, &pull.metadata)
	if err != nil {
		return preparedPull{}, err
	}
	pr := pull.metadata
	if pr.ID <= 0 || pr.Number <= 0 || pr.Base.Repo == nil || pr.Base.Repo.ID <= 0 {
		return preparedPull{}, &APIError{Code: InvalidResponse}
	}
	if pr.Number != scope.PullRequest || pr.Base.Repo.ID != scope.RepositoryID {
		return preparedPull{}, &APIError{Code: ScopeMismatch}
	}
	return pull, nil
}

func validRepositorySegment(value string) bool {
	if value == "" || len(value) > 255 || value == "." || value == ".." {
		return false
	}
	for _, r := range value {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') &&
			r != '-' && r != '_' && r != '.' {
			return false
		}
	}
	return true
}

func repoPath(repository Repository) string {
	return "/repos/" + url.PathEscape(repository.Owner.Login) + "/" + url.PathEscape(repository.Name)
}

func pullPath(repository Repository, number int) string {
	return repoPath(repository) + "/pulls/" + strconv.Itoa(number)
}
