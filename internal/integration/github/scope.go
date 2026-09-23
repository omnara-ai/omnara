package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
)

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

// Resolve names on every operation because a cached token can survive repository renames.
// GitHub App tokens can read public repositories beyond their installation.
// Name-addressed diff/page reads retain a rename/name-reuse race after repository-ID
// validation because those responses have no repository ID to recheck.
// https://docs.github.com/en/apps/using-github-apps/installing-a-github-app-from-a-third-party
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
	if listing.TotalCount == nil || *listing.TotalCount < 0 || listing.Repositories == nil ||
		len(listing.Repositories) > 100 {
		return preparedPull{}, &APIError{Code: InvalidResponse}
	}
	next, err := c.nextPage(header.Values("Link"), PageOptions{Page: 1, PerPage: 100}, "/installation/repositories")
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
