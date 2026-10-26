package github

import (
	"context"
	"errors"
	"net/http"
	"strconv"
)

var ErrChecksPermissionRequired = errors.New("github App lacks checks write permission")

type CheckRun struct {
	Name       string
	DetailsURL string
	Conclusion string
	Title      string
	Summary    string
}

type checkRunOutput struct {
	Title   string `json:"title"`
	Summary string `json:"summary"`
}

func (c *Client) CreateCheckRun(ctx context.Context, scope Scope, check CheckRun) (int64, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	pull, token, err := c.prepareChecks(ctx, scope)
	if err != nil {
		return 0, err
	}
	status := "in_progress"
	if check.Conclusion != "" {
		status = "completed"
	}
	var run struct {
		ID int64 `json:"id"`
	}
	_, err = c.request(ctx, token, http.MethodPost, repoPath(pull.repository)+"/check-runs", struct {
		Name       string         `json:"name"`
		HeadSHA    string         `json:"head_sha"`
		Status     string         `json:"status"`
		Conclusion string         `json:"conclusion,omitempty"`
		DetailsURL string         `json:"details_url,omitempty"`
		Output     checkRunOutput `json:"output"`
	}{check.Name, pull.metadata.Head.SHA, status, check.Conclusion, check.DetailsURL,
		checkRunOutput{check.Title, check.Summary}}, &run)
	if err != nil {
		return 0, err
	}
	if run.ID <= 0 {
		return 0, &APIError{Code: DeliveryUnknown}
	}
	return run.ID, nil
}

func (c *Client) CompleteCheckRun(ctx context.Context, scope Scope, checkRunID int64, check CheckRun) error {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	pull, token, err := c.prepareChecks(ctx, scope)
	if err != nil {
		return err
	}
	var run struct{}
	_, err = c.request(ctx, token, http.MethodPatch,
		repoPath(pull.repository)+"/check-runs/"+strconv.FormatInt(checkRunID, 10), struct {
			Conclusion string         `json:"conclusion"`
			Output     checkRunOutput `json:"output"`
		}{check.Conclusion, checkRunOutput{check.Title, check.Summary}}, &run)
	return err
}

func (c *Client) prepareChecks(ctx context.Context, scope Scope) (preparedPull, string, error) {
	pull, err := c.preparePull(ctx, scope, false)
	if err != nil {
		return preparedPull{}, "", err
	}
	token, err := c.mintInstallationToken(ctx, scope.RepositoryID, map[string]string{
		"checks": "write", "metadata": "read",
	})
	var apiErr *APIError
	if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusUnprocessableEntity {
		return preparedPull{}, "", ErrChecksPermissionRequired
	}
	if err != nil {
		return preparedPull{}, "", err
	}
	return pull, token.token, nil
}
