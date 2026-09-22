package github

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

type PageOptions struct {
	Page    int
	PerPage int
}

func readPage[T any](
	ctx context.Context, c *Client, scope Scope, suffix string, options PageOptions,
) ([]T, int, error) {
	ctx, cancel := context.WithTimeout(ctx, OperationTimeout)
	defer cancel()
	options, err := canonicalPageOptions(options)
	if err != nil {
		return nil, 0, err
	}
	pull, err := c.preparePull(ctx, scope, false)
	if err != nil {
		return nil, 0, err
	}
	path := repoPath(pull.repository) + suffix
	query := url.Values{"page": {strconv.Itoa(options.Page)}, "per_page": {strconv.Itoa(options.PerPage)}}
	var output []T
	header, err := c.request(ctx, pull.token, http.MethodGet, path+"?"+query.Encode(), nil, &output)
	if err != nil {
		return nil, 0, err
	}
	if output == nil || len(output) > options.PerPage {
		return nil, 0, &APIError{Code: InvalidResponse}
	}
	next, err := c.nextPage(header.Values("Link"), path, options)
	if err != nil {
		return nil, 0, err
	}
	return output, next, nil
}

func canonicalPageOptions(options PageOptions) (PageOptions, error) {
	if options.Page == 0 {
		options.Page = 1
	}
	if options.PerPage == 0 {
		options.PerPage = 30
	}
	if options.Page < 1 || options.PerPage < 1 || options.PerPage > 100 {
		return PageOptions{}, errors.New("github page must be positive and per_page between 1 and 100")
	}
	return options, nil
}

func (c *appClient) sameEndpoint(u *url.URL, path string) bool {
	return u != nil && u.Scheme == c.base.Scheme && strings.EqualFold(u.Host, c.base.Host) &&
		u.User == nil && u.Opaque == "" && u.Fragment == "" && !u.ForceQuery &&
		strings.EqualFold(u.EscapedPath(), path)
}

func (c *appClient) nextPage(headers []string, path string, options PageOptions) (int, error) {
	next := 0
	for _, header := range headers {
		for _, link := range strings.Split(header, ",") {
			parts := strings.Split(link, ";")
			isNext := false
			for _, parameter := range parts[1:] {
				key, value, ok := strings.Cut(strings.TrimSpace(parameter), "=")
				if ok && key == "rel" {
					for _, relation := range strings.Fields(strings.Trim(value, `"`)) {
						isNext = isNext || relation == "next"
					}
				}
			}
			if !isNext {
				continue
			}
			raw := strings.TrimSpace(parts[0])
			if next != 0 || !strings.HasPrefix(raw, "<") || !strings.HasSuffix(raw, ">") {
				return 0, &APIError{Code: InvalidResponse}
			}
			u, err := url.Parse(raw[1 : len(raw)-1])
			if err != nil || !c.sameEndpoint(u, path) {
				return 0, &APIError{Code: InvalidResponse}
			}
			query, err := url.ParseQuery(u.RawQuery)
			if err != nil || len(query) != 2 || len(query["page"]) != 1 || len(query["per_page"]) != 1 ||
				query.Get("per_page") != strconv.Itoa(options.PerPage) {
				return 0, &APIError{Code: InvalidResponse}
			}
			page, err := strconv.Atoi(query.Get("page"))
			if err != nil || page <= options.Page || page-options.Page != 1 {
				return 0, &APIError{Code: InvalidResponse}
			}
			next = page
		}
	}
	return next, nil
}
