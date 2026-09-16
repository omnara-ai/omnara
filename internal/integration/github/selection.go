package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type Installation struct {
	ID          string
	AppID       string
	Account     Account
	Permissions Permissions
	Suspended   bool
}

type Repository struct {
	ID       string
	NodeID   string
	Name     string
	FullName string
	Owner    Account
	Private  bool
	Admin    bool
}

type InstallationPage struct {
	Installations []Installation
	NextPage      int // Zero means the provider reports no further page.
}

type RepositoryPage struct {
	Repositories []Repository
	NextPage     int
}

// VerifiedSelection is a fresh setup proof, not ongoing runtime authority. The
// caller must recheck its own App/project lifecycle before committing these facts.
type VerifiedSelection struct {
	App          App
	Installation Installation
	Repository   Repository
}

type nativeInstallation struct {
	ID          nativeID      `json:"id"`
	AppID       nativeID      `json:"app_id"`
	Account     nativeAccount `json:"account"`
	Permissions Permissions   `json:"permissions"`
	SuspendedAt *string       `json:"suspended_at"`
}

func (c *Client) installation(raw nativeInstallation) (Installation, error) {
	if validID(string(raw.ID)) && !supportedRuntimeID(string(raw.ID)) {
		return Installation{}, ErrUnsupportedID
	}
	if string(raw.AppID) != c.appID {
		return Installation{}, ErrAppMismatch
	}
	account, err := raw.Account.project()
	if err != nil || !validID(string(raw.ID)) {
		return Installation{}, ErrInvalidResponse
	}
	return Installation{
		ID: string(raw.ID), AppID: string(raw.AppID), Account: account,
		Permissions: raw.Permissions, Suspended: raw.SuspendedAt != nil,
	}, nil
}

type nativeRepository struct {
	ID          nativeID      `json:"id"`
	NodeID      string        `json:"node_id"`
	Name        string        `json:"name"`
	FullName    string        `json:"full_name"`
	Owner       nativeAccount `json:"owner"`
	Private     bool          `json:"private"`
	Permissions struct {
		Admin bool `json:"admin"`
	} `json:"permissions"`
}

func (raw nativeRepository) project() (Repository, error) {
	owner, err := raw.Owner.project()
	if err != nil || !validID(string(raw.ID)) || !validNodeID(raw.NodeID) ||
		!validRepositoryOwner(owner.Login) || !validRepositoryName(raw.Name) || raw.FullName != owner.Login+"/"+raw.Name {
		return Repository{}, ErrInvalidResponse
	}
	if !supportedRuntimeID(string(raw.ID)) {
		return Repository{}, ErrUnsupportedID
	}
	return Repository{
		ID: string(raw.ID), NodeID: raw.NodeID, Name: raw.Name, FullName: raw.FullName, Owner: owner,
		Private: raw.Private, Admin: raw.Permissions.Admin,
	}, nil
}

func (c *Client) ListInstallations(ctx context.Context, user UserOAuth, page int) (InstallationPage, error) {
	if err := validateOAuth(user); err != nil {
		return InstallationPage{}, err
	}
	if !validPage(page) {
		return InstallationPage{}, ErrInvalidInput
	}
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	var response struct {
		Installations []nativeInstallation `json:"installations"`
		TotalCount    *int64               `json:"total_count"`
	}
	if err := c.get(ctx, pagePath("/user/installations", page), user.AccessToken, &response); err != nil {
		return InstallationPage{}, err
	}
	if response.Installations == nil {
		return InstallationPage{}, ErrInvalidResponse
	}
	next, err := nextPage(page, len(response.Installations), response.TotalCount)
	if err != nil {
		return InstallationPage{}, err
	}
	result := InstallationPage{Installations: make([]Installation, 0, len(response.Installations)), NextPage: next}
	for _, raw := range response.Installations {
		installation, err := c.installation(raw)
		if err != nil {
			return InstallationPage{}, err
		}
		result.Installations = append(result.Installations, installation)
	}
	return result, nil
}

func (c *Client) ListRepositories(
	ctx context.Context, user UserOAuth, installationID string, page int,
) (RepositoryPage, error) {
	if err := validateOAuth(user); err != nil {
		return RepositoryPage{}, err
	}
	if !validID(installationID) || !validPage(page) {
		return RepositoryPage{}, ErrInvalidInput
	}
	if !supportedRuntimeID(installationID) {
		return RepositoryPage{}, ErrUnsupportedID
	}
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	token, err := c.appJWT()
	if err != nil {
		return RepositoryPage{}, err
	}
	if _, err := c.getInstallation(ctx, token, installationID); err != nil {
		return RepositoryPage{}, err
	}
	return c.repositories(ctx, user.AccessToken, installationID, page)
}

func (c *Client) repositories(
	ctx context.Context, token, installationID string, page int,
) (RepositoryPage, error) {
	var response struct {
		Repositories []nativeRepository `json:"repositories"`
		TotalCount   *int64             `json:"total_count"`
	}
	path := "/user/installations/" + installationID + "/repositories"
	if err := c.get(ctx, pagePath(path, page), token, &response); err != nil {
		return RepositoryPage{}, err
	}
	if response.Repositories == nil {
		return RepositoryPage{}, ErrInvalidResponse
	}
	next, err := nextPage(page, len(response.Repositories), response.TotalCount)
	if err != nil {
		return RepositoryPage{}, err
	}
	result := RepositoryPage{Repositories: make([]Repository, 0, len(response.Repositories)), NextPage: next}
	for _, raw := range response.Repositories {
		repository, err := raw.project()
		if err != nil {
			return RepositoryPage{}, err
		}
		result.Repositories = append(result.Repositories, repository)
	}
	return result, nil
}

// VerifySelection uses three fresh, bounded reads: App identity, the user's
// exact repository/admin role, and that repository's App installation. The name
// is an untrusted locator, never authority. Renames require refreshing the picker;
// redirects and a different returned ID/name cannot silently change the selection.
// https://docs.github.com/en/rest/repos/repos#get-a-repository
// https://docs.github.com/en/rest/apps/apps#get-a-repository-installation-for-the-authenticated-app
func (c *Client) VerifySelection(
	ctx context.Context, user UserOAuth, installationID, repositoryID, repositoryFullName string,
) (VerifiedSelection, error) {
	if err := validateOAuth(user); err != nil {
		return VerifiedSelection{}, err
	}
	if !validID(installationID) || !validID(repositoryID) {
		return VerifiedSelection{}, ErrInvalidInput
	}
	if !supportedRuntimeID(installationID) || !supportedRuntimeID(repositoryID) {
		return VerifiedSelection{}, ErrUnsupportedID
	}
	owner, name, found := strings.Cut(repositoryFullName, "/")
	if !found || !validRepositoryOwner(owner) || !validRepositoryName(name) || name == "." || name == ".." {
		return VerifiedSelection{}, ErrInvalidInput
	}
	path := "/repos/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
	ctx, cancel := context.WithTimeout(ctx, setupTimeout)
	defer cancel()
	token, err := c.appJWT()
	if err != nil {
		return VerifiedSelection{}, err
	}
	app, err := c.verifyApp(ctx, token)
	if err != nil {
		return VerifiedSelection{}, err
	}
	var rawRepository nativeRepository
	if err := c.get(ctx, path, user.AccessToken, &rawRepository); err != nil {
		var apiError *APIError
		if errors.As(err, &apiError) &&
			(apiError.StatusCode == http.StatusMovedPermanently || apiError.StatusCode == http.StatusNotFound) {
			return VerifiedSelection{}, ErrSelectionNotAccessible
		}
		return VerifiedSelection{}, err
	}
	repository, err := rawRepository.project()
	if err != nil {
		return VerifiedSelection{}, err
	}
	if repository.ID != repositoryID || !strings.EqualFold(repository.FullName, repositoryFullName) {
		return VerifiedSelection{}, ErrSelectionNotAccessible
	}
	if !repository.Admin {
		return VerifiedSelection{}, ErrRepositoryAdminRequired
	}
	var rawInstallation nativeInstallation
	if err := c.get(ctx, path+"/installation", token, &rawInstallation); err != nil {
		return VerifiedSelection{}, err
	}
	installation, err := c.selectedInstallation(rawInstallation, installationID)
	if err != nil {
		return VerifiedSelection{}, err
	}
	if !installation.Permissions.sufficient() {
		return VerifiedSelection{}, ErrMissingPermissions
	}
	if repository.Owner.ID != installation.Account.ID {
		return VerifiedSelection{}, ErrInvalidResponse
	}
	return VerifiedSelection{App: app, Installation: installation, Repository: repository}, nil
}

func (c *Client) getInstallation(ctx context.Context, token, id string) (Installation, error) {
	var response nativeInstallation
	if err := c.get(ctx, "/app/installations/"+id, token, &response); err != nil {
		return Installation{}, err
	}
	return c.selectedInstallation(response, id)
}

func (c *Client) selectedInstallation(response nativeInstallation, id string) (Installation, error) {
	installation, err := c.installation(response)
	if err != nil {
		return Installation{}, err
	}
	if installation.ID != id {
		return Installation{}, ErrInvalidResponse
	}
	if installation.Suspended {
		return Installation{}, ErrInstallationUnavailable
	}
	return installation, nil
}

func validPage(page int) bool {
	return page > 0 && page <= 1_000_000
}

func pagePath(path string, page int) string {
	return path + "?per_page=" + strconv.Itoa(pageSize) + "&page=" + strconv.Itoa(page)
}

func nextPage(page, count int, total *int64) (int, error) {
	if total == nil || *total < 0 || count > pageSize || int64(count) > *total {
		return 0, ErrInvalidResponse
	}
	if int64(page-1)*pageSize+int64(count) >= *total {
		return 0, nil
	}
	if count != pageSize {
		return 0, ErrInvalidResponse
	}
	return page + 1, nil
}
