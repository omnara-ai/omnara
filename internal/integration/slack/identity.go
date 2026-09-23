package slack

import (
	"context"
	"errors"
)

func CheckIdentity(ctx context.Context, config OAuthConfig, token string, expected Identity) error {
	// Secret rotation can change a token without changing the saved app identity.
	if token == "" || expected.WorkspaceID == "" || expected.BotUserID == "" {
		return errors.New("slack token and expected workspace/bot identity are required")
	}
	var response struct {
		OK     bool   `json:"ok"`
		Error  string `json:"error"`
		TeamID string `json:"team_id"`
		UserID string `json:"user_id"`
		BotID  string `json:"bot_id"`
	}
	result, err := callFormAt(ctx, config.HTTPClient, config.APIURL, token, "auth.test", nil, &response)
	if err != nil {
		return err
	}
	if result != (APIResult{}) {
		return apiResultError("slack identity verification", result)
	}
	if !response.OK {
		return apiResultError("slack identity verification", ErrorResult(response.Error))
	}
	if response.TeamID != expected.WorkspaceID || response.UserID != expected.BotUserID || response.BotID == "" {
		return errors.New("slack token does not match the app's workspace and bot identity")
	}
	return nil
}
