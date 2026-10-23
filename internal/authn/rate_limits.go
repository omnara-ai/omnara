package authn

// ClientRateLimits sets per-client authentication budgets.
// Fields with a value of 0 use the default limit.
type ClientRateLimits struct {
	Signup              int
	Login               int
	PasswordReset       int
	Token               int
	OAuthLogin          int
	OAuthLoginConnector int
	DeviceAuthorization int
	DevicePoll          int
}

func (limits ClientRateLimits) WithDefaults() ClientRateLimits {
	for _, setting := range []struct {
		value    *int
		fallback int
	}{
		{&limits.Signup, 500},
		{&limits.Login, 1000},
		{&limits.PasswordReset, 500},
		{&limits.Token, 5000},
		{&limits.OAuthLogin, 2000},
		{&limits.OAuthLoginConnector, 1000},
		{&limits.DeviceAuthorization, 1000},
		{&limits.DevicePoll, 30000},
	} {
		if *setting.value == 0 {
			*setting.value = setting.fallback
		}
	}
	return limits
}
