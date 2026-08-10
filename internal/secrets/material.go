package secrets

import "fmt"

type Material interface {
	secretMaterial()
}

type GenericMaterial struct {
	Value string
}

func (GenericMaterial) secretMaterial() {}

type OAuthRefreshMaterial struct {
	RefreshToken  string
	TokenEndpoint string
	ClientID      string
	ClientSecret  string
	Resource      string
}

type OAuthTokenSetMaterial struct {
	AccessToken         string
	AccessTokenLifetime OAuthAccessTokenLifetime
	Refresh             *OAuthRefreshMaterial
	IDToken             string
	MCPURL              string
	Scopes              string
	TokenType           string
}

func (OAuthTokenSetMaterial) secretMaterial() {}

type SlackAppCredentialsMaterial struct {
	AccessToken   string
	ClientID      string
	ClientSecret  string
	SigningSecret string
}

func (SlackAppCredentialsMaterial) secretMaterial() {}

type AWSCredentialsMaterial struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	RoleARN         string
	ExternalID      string
}

func (AWSCredentialsMaterial) secretMaterial() {}

type CanonicalMaterial struct {
	Kind                     Kind
	Payload                  Payload
	OAuthAccessTokenLifetime OAuthAccessTokenLifetime
}

func CanonicalizeMaterial(material Material) (CanonicalMaterial, error) {
	var canonical CanonicalMaterial
	switch value := material.(type) {
	case GenericMaterial:
		canonical.Kind = KindGeneric
		canonical.Payload = Payload{KeyValue: value.Value}
	case OAuthTokenSetMaterial:
		canonical.Kind = KindOAuthTokenSet
		canonical.Payload = Payload{KeyAccessToken: value.AccessToken}
		canonical.OAuthAccessTokenLifetime = value.AccessTokenLifetime
		setMaterialValue(canonical.Payload, KeyIDToken, value.IDToken)
		setMaterialValue(canonical.Payload, KeyMCPURL, value.MCPURL)
		setMaterialValue(canonical.Payload, KeyScopes, value.Scopes)
		setMaterialValue(canonical.Payload, KeyTokenType, value.TokenType)
		if value.Refresh != nil {
			setMaterialValue(canonical.Payload, KeyRefreshToken, value.Refresh.RefreshToken)
			setMaterialValue(canonical.Payload, KeyTokenEndpoint, value.Refresh.TokenEndpoint)
			setMaterialValue(canonical.Payload, KeyClientID, value.Refresh.ClientID)
			setMaterialValue(canonical.Payload, KeyClientSecret, value.Refresh.ClientSecret)
			setMaterialValue(canonical.Payload, KeyResource, value.Refresh.Resource)
		}
	case SlackAppCredentialsMaterial:
		canonical.Kind = KindSlackAppCredentials
		canonical.Payload = Payload{
			KeyAccessToken:   value.AccessToken,
			KeyClientID:      value.ClientID,
			KeyClientSecret:  value.ClientSecret,
			KeySigningSecret: value.SigningSecret,
		}
	case AWSCredentialsMaterial:
		canonical.Kind = KindAWSCredentials
		canonical.Payload = Payload{
			KeyAWSAccessKeyID:     value.AccessKeyID,
			KeyAWSSecretAccessKey: value.SecretAccessKey,
		}
		setMaterialValue(canonical.Payload, KeyAWSSessionToken, value.SessionToken)
		setMaterialValue(canonical.Payload, KeyAWSRoleARN, value.RoleARN)
		setMaterialValue(canonical.Payload, KeyAWSExternalID, value.ExternalID)
	default:
		return CanonicalMaterial{}, fmt.Errorf("unsupported secret material %T", material)
	}
	if _, err := ValidatePayload(canonical.Kind, canonical.Payload); err != nil {
		return CanonicalMaterial{}, err
	}
	return canonical, nil
}

func OAuthTokenSetMaterialFromPayload(
	payload Payload,
	lifetime OAuthAccessTokenLifetime,
) (OAuthTokenSetMaterial, error) {
	if _, err := ValidatePayload(KindOAuthTokenSet, payload); err != nil {
		return OAuthTokenSetMaterial{}, err
	}
	material := OAuthTokenSetMaterial{
		AccessToken:         payload[KeyAccessToken],
		AccessTokenLifetime: lifetime,
		IDToken:             payload[KeyIDToken],
		MCPURL:              payload[KeyMCPURL],
		Scopes:              payload[KeyScopes],
		TokenType:           payload[KeyTokenType],
	}
	if payload[KeyRefreshToken] != "" {
		material.Refresh = &OAuthRefreshMaterial{
			RefreshToken:  payload[KeyRefreshToken],
			TokenEndpoint: payload[KeyTokenEndpoint],
			ClientID:      payload[KeyClientID],
			ClientSecret:  payload[KeyClientSecret],
			Resource:      payload[KeyResource],
		}
	}
	return material, nil
}

func SlackAppCredentialsMaterialFromPayload(payload Payload) SlackAppCredentialsMaterial {
	return SlackAppCredentialsMaterial{
		AccessToken:   payload[KeyAccessToken],
		ClientID:      payload[KeyClientID],
		ClientSecret:  payload[KeyClientSecret],
		SigningSecret: payload[KeySigningSecret],
	}
}

func setMaterialValue(payload Payload, key, value string) {
	if value != "" {
		payload[key] = value
	}
}
