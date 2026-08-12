package machinedaemon

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
)

const maxFailureDetailBytes = 4 * 1024

type UpdateFailureReport struct {
	DaemonVersion string
	TargetVersion string
	Detail        string
}

func (c *Client) ReportUpdateFailure(ctx context.Context, report UpdateFailureReport) error {
	if report.DaemonVersion == "" {
		return errors.New("daemon version is required for update failure reports")
	}
	query := url.Values{}
	query.Set("stage", "daemon_update")
	query.Set("daemon_version", report.DaemonVersion)
	if report.TargetVersion != "" {
		query.Set("target_version", report.TargetVersion)
	}
	return c.postFailureReport(ctx, query, report.Detail)
}

func (c *Client) ReportUninstall(ctx context.Context, failureDetail string) error {
	stage := "daemon_uninstalled"
	if failureDetail != "" {
		stage = "daemon_uninstall"
	}
	query := url.Values{}
	query.Set("stage", stage)
	return c.postFailureReport(ctx, query, failureDetail)
}

func (c *Client) postFailureReport(ctx context.Context, query url.Values, detail string) error {
	if len(detail) > maxFailureDetailBytes {
		detail = detail[len(detail)-maxFailureDetailBytes:]
		query.Set("capture_status", "1")
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		c.cfg.APIURL+daemonAPIPath+"/failures?"+query.Encode(),
		strings.NewReader(detail),
	)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.cfg.MachineToken)
	req.Header.Set("Content-Type", "text/plain")
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	data, readErr := readDaemonHTTPResponse(resp.Body)
	closeErr := resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return errors.Join(newHTTPStatusError(resp, data), readErr, closeErr)
	}
	return errors.Join(readErr, closeErr)
}
