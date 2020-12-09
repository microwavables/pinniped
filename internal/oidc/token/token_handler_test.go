// Copyright 2020 the Pinniped contributors. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

package token

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"encoding/json"
	"io"
	"io/ioutil"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/ory/fosite"
	"github.com/ory/fosite/handler/oauth2"
	"github.com/ory/fosite/handler/openid"
	"github.com/ory/fosite/handler/pkce"
	"github.com/ory/fosite/token/jwt"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/require"
	"gopkg.in/square/go-jose.v2"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/kubernetes/fake"
	v1 "k8s.io/client-go/kubernetes/typed/core/v1"

	"go.pinniped.dev/internal/crud"
	"go.pinniped.dev/internal/fositestorage/accesstoken"
	"go.pinniped.dev/internal/fositestorage/authorizationcode"
	"go.pinniped.dev/internal/fositestorage/openidconnect"
	storagepkce "go.pinniped.dev/internal/fositestorage/pkce"
	"go.pinniped.dev/internal/fositestorage/refreshtoken"
	"go.pinniped.dev/internal/here"
	"go.pinniped.dev/internal/oidc"
	"go.pinniped.dev/internal/oidc/jwks"
	"go.pinniped.dev/internal/oidc/oidctestutil"
	"go.pinniped.dev/internal/testutil"
)

const (
	goodIssuer           = "https://some-issuer.com"
	goodClient           = "pinniped-cli"
	goodRedirectURI      = "http://127.0.0.1/callback"
	goodPKCECodeVerifier = "some-pkce-verifier-that-must-be-at-least-43-characters-to-meet-entropy-requirements"
	goodNonce            = "some-nonce-that-is-at-least-32-characters-to-meet-entropy-requirements"
	goodSubject          = "some-subject"
	goodUsername         = "some-username"

	hmacSecret = "this needs to be at least 32 characters to meet entropy requirements"

	authCodeExpirationSeconds    = 3 * 60 // Current, we set our auth code expiration to 3 minutes
	accessTokenExpirationSeconds = 5 * 60 // Currently, we set our access token expiration to 5 minutes
	idTokenExpirationSeconds     = 5 * 60 // Currently, we set our ID token expiration to 5 minutes

	timeComparisonFudgeSeconds = 15
)

var (
	goodAuthTime        = time.Date(1, 2, 3, 4, 5, 6, 7, time.UTC)
	goodRequestedAtTime = time.Date(7, 6, 5, 4, 3, 2, 1, time.UTC)

	fositeInvalidMethodErrorBody = func(actual string) string {
		return here.Docf(`
			{
				"error":             "invalid_request",
				"error_verbose":     "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed",
				"error_description": "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed\n\nHTTP method is \"%s\", expected \"POST\".",
				"error_hint":        "HTTP method is \"%s\", expected \"POST\".",
				"status_code":       400
			 }
		`, actual, actual)
	}

	fositeMissingGrantTypeErrorBody = here.Docf(`
		{
			"error":             "invalid_request",
			"error_verbose":     "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed",
			"error_description": "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed\n\nRequest parameter \"grant_type\"\" is missing",
			"error_hint":        "Request parameter \"grant_type\"\" is missing",
			"status_code":       400
		}
	`)

	fositeEmptyPayloadErrorBody = here.Doc(`
		{
			"error":             "invalid_request",
			"error_verbose":     "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed",
			"error_description": "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed\n\nThe POST body can not be empty.",
			"error_hint":        "The POST body can not be empty.",
			"status_code":       400
		}
	`)

	fositeInvalidPayloadErrorBody = here.Doc(`
		{
			"error":             "invalid_request",
			"error_verbose":     "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed",
			"error_description": "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed\n\nUnable to parse HTTP body, make sure to send a properly formatted form request body.",
			"error_hint":        "Unable to parse HTTP body, make sure to send a properly formatted form request body.",
			"status_code":       400
		}
	`)

	fositeInvalidRequestErrorBody = here.Doc(`
		{
			"error":             "invalid_request",
			"error_verbose":     "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed",
			"error_description": "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed\n\nMake sure that the various parameters are correct, be aware of case sensitivity and trim your parameters. Make sure that the client you are using has exactly whitelisted the redirect_uri you specified.",
			"error_hint":        "Make sure that the various parameters are correct, be aware of case sensitivity and trim your parameters. Make sure that the client you are using has exactly whitelisted the redirect_uri you specified.",
			"status_code":       400
		}
	`)

	fositeMissingClientErrorBody = here.Doc(`
		{
			"error":             "invalid_request",
			"error_verbose":     "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed",
			"error_description": "The request is missing a required parameter, includes an invalid parameter value, includes a parameter more than once, or is otherwise malformed\n\nClient credentials missing or malformed in both HTTP Authorization header and HTTP POST body.",
			"error_hint":        "Client credentials missing or malformed in both HTTP Authorization header and HTTP POST body.",
			"status_code":       400
		}
	`)

	fositeInvalidClientErrorBody = here.Doc(`
		{
			"error":             "invalid_client",
			"error_verbose":     "Client authentication failed (e.g., unknown client, no client authentication included, or unsupported authentication method)",
			"error_description": "Client authentication failed (e.g., unknown client, no client authentication included, or unsupported authentication method)",
			"status_code":       401
		}
	`)

	fositeInvalidAuthCodeErrorBody = here.Doc(`
		{
			"error":             "invalid_grant",
			"error_verbose":     "The provided authorization grant (e.g., authorization code, resource owner credentials) or refresh token is invalid, expired, revoked, does not match the redirection URI used in the authorization request, or was issued to another client",
			"error_description": "The provided authorization grant (e.g., authorization code, resource owner credentials) or refresh token is invalid, expired, revoked, does not match the redirection URI used in the authorization request, or was issued to another client",
			"status_code":       400
		}
	`)

	fositeReusedAuthCodeErrorBody = here.Doc(`
		{
			"error":             "invalid_grant",
			"error_verbose":     "The provided authorization grant (e.g., authorization code, resource owner credentials) or refresh token is invalid, expired, revoked, does not match the redirection URI used in the authorization request, or was issued to another client",
			"error_description": "The provided authorization grant (e.g., authorization code, resource owner credentials) or refresh token is invalid, expired, revoked, does not match the redirection URI used in the authorization request, or was issued to another client\n\nThe authorization code has already been used.",
			"error_hint":        "The authorization code has already been used.",
			"status_code":       400
		}
	`)

	fositeInvalidRedirectURIErrorBody = here.Doc(`
		{
			"error":             "invalid_grant",
			"error_verbose":     "The provided authorization grant (e.g., authorization code, resource owner credentials) or refresh token is invalid, expired, revoked, does not match the redirection URI used in the authorization request, or was issued to another client",
			"error_description": "The provided authorization grant (e.g., authorization code, resource owner credentials) or refresh token is invalid, expired, revoked, does not match the redirection URI used in the authorization request, or was issued to another client\n\nThe \"redirect_uri\" from this request does not match the one from the authorize request.",
			"error_hint":        "The \"redirect_uri\" from this request does not match the one from the authorize request.",
			"status_code":       400
		}
	`)

	fositeMissingPKCEVerifierErrorBody = here.Doc(`
		{
			"error":             "invalid_grant",
			"error_verbose":     "The provided authorization grant (e.g., authorization code, resource owner credentials) or refresh token is invalid, expired, revoked, does not match the redirection URI used in the authorization request, or was issued to another client",
			"error_description": "The provided authorization grant (e.g., authorization code, resource owner credentials) or refresh token is invalid, expired, revoked, does not match the redirection URI used in the authorization request, or was issued to another client\n\nThe PKCE code verifier must be at least 43 characters.",
			"error_hint":        "The PKCE code verifier must be at least 43 characters.",
			"status_code":       400
		}
	`)

	fositeWrongPKCEVerifierErrorBody = here.Doc(`
		{
			"error":             "invalid_grant",
			"error_verbose":     "The provided authorization grant (e.g., authorization code, resource owner credentials) or refresh token is invalid, expired, revoked, does not match the redirection URI used in the authorization request, or was issued to another client",
			"error_description": "The provided authorization grant (e.g., authorization code, resource owner credentials) or refresh token is invalid, expired, revoked, does not match the redirection URI used in the authorization request, or was issued to another client\n\nThe PKCE code challenge did not match the code verifier.",
			"error_hint":        "The PKCE code challenge did not match the code verifier.",
			"status_code":       400
		}
	`)

	fositeTemporarilyUnavailableErrorBody = here.Doc(`
		{
		  "error": "temporarily_unavailable",
		  "error_description": "The authorization server is currently unable to handle the request due to a temporary overloading or maintenance of the server",
		  "error_verbose": "The authorization server is currently unable to handle the request due to a temporary overloading or maintenance of the server",
		  "status_code": 503
		}
	`)

	happyAuthRequest = &http.Request{
		Form: url.Values{
			"response_type":         {"code"},
			"scope":                 {"openid profile email"},
			"client_id":             {goodClient},
			"state":                 {"some-state-value-that-is-32-byte"},
			"nonce":                 {goodNonce},
			"code_challenge":        {testutil.SHA256(goodPKCECodeVerifier)},
			"code_challenge_method": {"S256"},
			"redirect_uri":          {goodRedirectURI},
		},
	}

	happyTokenExchangeRequest = func(audience string, subjectToken string) *http.Request {
		return &http.Request{
			Form: url.Values{
				"grant_type":           {"urn:ietf:params:oauth:grant-type:token-exchange"},
				"audience":             {audience},
				"subject_token":        {subjectToken},
				"subject_token_type":   {"urn:ietf:params:oauth:token-type:access_token"},
				"requested_token_type": {"urn:ietf:params:oauth:token-type:jwt"},
				"client_id":            {goodClient},
			},
		}
	}
)

type authcodeExchangeInputs struct {
	modifyAuthRequest  func(authRequest *http.Request)
	modifyTokenRequest func(r *http.Request, authCode string)
	modifyStorage      func(
		t *testing.T,
		s interface {
			oauth2.TokenRevocationStorage
			oauth2.CoreStorage
			openid.OpenIDConnectRequestStorage
			pkce.PKCERequestStorage
			fosite.ClientManager
		},
		authCode string,
	)
	makeOathHelper func(
		t *testing.T,
		authRequest *http.Request,
		store interface {
			oauth2.TokenRevocationStorage
			oauth2.CoreStorage
			openid.OpenIDConnectRequestStorage
			pkce.PKCERequestStorage
			fosite.ClientManager
		},
	) (fosite.OAuth2Provider, string, *ecdsa.PrivateKey)

	wantStatus            int
	wantSuccessBodyFields []string
	wantErrorResponseBody string
	wantRequestedScopes   []string
	wantGrantedScopes     []string
}

func TestTokenEndpoint(t *testing.T) {
	tests := []struct {
		name             string
		authcodeExchange authcodeExchangeInputs
	}{
		// happy path
		{
			name: "request is valid and tokens are issued",
			authcodeExchange: authcodeExchangeInputs{
				wantStatus:            http.StatusOK,
				wantSuccessBodyFields: []string{"id_token", "access_token", "token_type", "scope", "expires_in"}, // no refresh token
				wantRequestedScopes:   []string{"openid", "profile", "email"},
				wantGrantedScopes:     []string{"openid"},
			},
		},
		{
			name: "openid scope was not requested from authorize endpoint",
			authcodeExchange: authcodeExchangeInputs{
				modifyAuthRequest: func(authRequest *http.Request) {
					authRequest.Form.Set("scope", "profile email")
				},
				wantStatus:            http.StatusOK,
				wantSuccessBodyFields: []string{"access_token", "token_type", "scope", "expires_in"}, // no id or refresh tokens
				wantRequestedScopes:   []string{"profile", "email"},
				wantGrantedScopes:     []string{},
			},
		},
		{
			name: "offline_access and openid scopes were requested and granted from authorize endpoint",
			authcodeExchange: authcodeExchangeInputs{
				modifyAuthRequest: func(authRequest *http.Request) {
					authRequest.Form.Set("scope", "openid offline_access")
				},
				wantStatus:            http.StatusOK,
				wantSuccessBodyFields: []string{"id_token", "access_token", "token_type", "scope", "expires_in", "refresh_token"}, // all possible tokens
				wantRequestedScopes:   []string{"openid", "offline_access"},
				wantGrantedScopes:     []string{"openid", "offline_access"},
			},
		},
		{
			name: "offline_access (without openid scope) was requested and granted from authorize endpoint",
			authcodeExchange: authcodeExchangeInputs{
				modifyAuthRequest: func(authRequest *http.Request) {
					authRequest.Form.Set("scope", "offline_access")
				},
				wantStatus:            http.StatusOK,
				wantSuccessBodyFields: []string{"access_token", "token_type", "scope", "expires_in", "refresh_token"}, // no id token
				wantRequestedScopes:   []string{"offline_access"},
				wantGrantedScopes:     []string{"offline_access"},
			},
		},

		// sad path
		{
			name: "GET method is wrong",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest:    func(r *http.Request, authCode string) { r.Method = http.MethodGet },
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeInvalidMethodErrorBody("GET"),
			},
		},
		{
			name: "PUT method is wrong",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest:    func(r *http.Request, authCode string) { r.Method = http.MethodPut },
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeInvalidMethodErrorBody("PUT"),
			},
		},
		{
			name: "PATCH method is wrong",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest:    func(r *http.Request, authCode string) { r.Method = http.MethodPatch },
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeInvalidMethodErrorBody("PATCH"),
			},
		},
		{
			name: "DELETE method is wrong",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest:    func(r *http.Request, authCode string) { r.Method = http.MethodDelete },
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeInvalidMethodErrorBody("DELETE"),
			},
		},
		{
			name: "content type is invalid",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest:    func(r *http.Request, authCode string) { r.Header.Set("Content-Type", "text/plain") },
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeEmptyPayloadErrorBody,
			},
		},
		{
			name: "payload is not valid form serialization",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest: func(r *http.Request, authCode string) {
					r.Body = ioutil.NopCloser(strings.NewReader("this newline character is not allowed in a form serialization: \n"))
				},
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeMissingGrantTypeErrorBody,
			},
		},
		{
			name: "payload is empty",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest:    func(r *http.Request, authCode string) { r.Body = nil },
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeInvalidPayloadErrorBody,
			},
		},
		{
			name: "grant type is missing in request",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest: func(r *http.Request, authCode string) {
					r.Body = happyBody(authCode).WithGrantType("").ReadCloser()
				},
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeMissingGrantTypeErrorBody,
			},
		},
		{
			name: "grant type is not authorization_code",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest: func(r *http.Request, authCode string) {
					r.Body = happyBody(authCode).WithGrantType("bogus").ReadCloser()
				},
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeInvalidRequestErrorBody,
			},
		},
		{
			name: "client id is missing in request",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest: func(r *http.Request, authCode string) {
					r.Body = happyBody(authCode).WithClientID("").ReadCloser()
				},
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeMissingClientErrorBody,
			},
		},
		{
			name: "client id is wrong",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest: func(r *http.Request, authCode string) {
					r.Body = happyBody(authCode).WithClientID("bogus").ReadCloser()
				},
				wantStatus:            http.StatusUnauthorized,
				wantErrorResponseBody: fositeInvalidClientErrorBody,
			},
		},
		{
			name: "auth code is missing in request",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest: func(r *http.Request, authCode string) {
					r.Body = happyBody(authCode).WithAuthCode("").ReadCloser()
				},
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeInvalidAuthCodeErrorBody,
			},
		},
		{
			name: "auth code has never been valid",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest: func(r *http.Request, authCode string) {
					r.Body = happyBody(authCode).WithAuthCode("bogus").ReadCloser()
				},
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeInvalidAuthCodeErrorBody,
			},
		},
		{
			name: "auth code is invalidated",
			authcodeExchange: authcodeExchangeInputs{
				modifyStorage: func(
					t *testing.T,
					s interface {
						oauth2.TokenRevocationStorage
						oauth2.CoreStorage
						openid.OpenIDConnectRequestStorage
						pkce.PKCERequestStorage
						fosite.ClientManager
					},
					authCode string,
				) {
					err := s.InvalidateAuthorizeCodeSession(context.Background(), getFositeDataSignature(t, authCode))
					require.NoError(t, err)
				},
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeReusedAuthCodeErrorBody,
			},
		},
		{
			name: "redirect uri is missing in request",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest: func(r *http.Request, authCode string) {
					r.Body = happyBody(authCode).WithRedirectURI("").ReadCloser()
				},
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeInvalidRedirectURIErrorBody,
			},
		},
		{
			name: "redirect uri is wrong",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest: func(r *http.Request, authCode string) {
					r.Body = happyBody(authCode).WithRedirectURI("bogus").ReadCloser()
				},
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeInvalidRedirectURIErrorBody,
			},
		},
		{
			name: "pkce is missing in request",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest: func(r *http.Request, authCode string) {
					r.Body = happyBody(authCode).WithPKCE("").ReadCloser()
				},
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeMissingPKCEVerifierErrorBody,
			},
		},
		{
			name: "pkce is wrong",
			authcodeExchange: authcodeExchangeInputs{
				modifyTokenRequest: func(r *http.Request, authCode string) {
					r.Body = happyBody(authCode).WithPKCE(
						"bogus-verifier-that-is-at-least-43-characters-for-the-sake-of-entropy",
					).ReadCloser()
				},
				wantStatus:            http.StatusBadRequest,
				wantErrorResponseBody: fositeWrongPKCEVerifierErrorBody,
			},
		},
		{
			name: "private signing key for JWTs has not yet been provided by the controller who is responsible for dynamically providing it",
			authcodeExchange: authcodeExchangeInputs{
				makeOathHelper:        makeOauthHelperWithNilPrivateJWTSigningKey,
				wantStatus:            http.StatusServiceUnavailable,
				wantErrorResponseBody: fositeTemporarilyUnavailableErrorBody,
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			exchangeAuthcodeForTokens(t, test.authcodeExchange)
		})
	}
}

func TestTokenEndpointWhenAuthcodeIsUsedTwice(t *testing.T) {
	tests := []struct {
		name             string
		authcodeExchange authcodeExchangeInputs
	}{
		{
			name: "authcode exchange succeeds once and then fails when the same authcode is used again",
			authcodeExchange: authcodeExchangeInputs{
				modifyAuthRequest: func(authRequest *http.Request) {
					authRequest.Form.Set("scope", "openid offline_access profile email")
				},
				wantStatus:            http.StatusOK,
				wantSuccessBodyFields: []string{"id_token", "refresh_token", "access_token", "token_type", "expires_in", "scope"},
				wantRequestedScopes:   []string{"openid", "offline_access", "profile", "email"},
				wantGrantedScopes:     []string{"openid", "offline_access"},
			},
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			// First call - should be successful.
			subject, rsp, authCode, secrets, oauthStore := exchangeAuthcodeForTokens(t, test.authcodeExchange)
			var parsedResponseBody map[string]interface{}
			require.NoError(t, json.Unmarshal(rsp.Body.Bytes(), &parsedResponseBody))

			// Second call - should be unsuccessful since auth code was already used.
			//
			// Fosite will also revoke the access token as is recommended by the OIDC spec. Currently, we don't
			// delete the OIDC storage...but we probably should.
			req := httptest.NewRequest("POST", "/path/shouldn't/matter", happyBody(authCode).ReadCloser())
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			rsp1 := httptest.NewRecorder()
			subject.ServeHTTP(rsp1, req)
			t.Logf("second response: %#v", rsp1)
			t.Logf("second response body: %q", rsp1.Body.String())
			require.Equal(t, http.StatusBadRequest, rsp1.Code)
			testutil.RequireEqualContentType(t, rsp1.Header().Get("Content-Type"), "application/json")
			require.JSONEq(t, fositeReusedAuthCodeErrorBody, rsp1.Body.String())

			// This was previously invalidated by the first request, so it remains invalidated
			requireInvalidAuthCodeStorage(t, authCode, oauthStore)
			// Has now invalidated the access token that was previously handed out by the first request
			requireInvalidAccessTokenStorage(t, parsedResponseBody, oauthStore)
			// This was previously invalidated by the first request, so it remains invalidated
			requireInvalidPKCEStorage(t, authCode, oauthStore)
			// Fosite never cleans up OpenID Connect session storage, so it is still there
			requireValidOIDCStorage(t, parsedResponseBody, authCode, oauthStore, test.authcodeExchange.wantRequestedScopes, test.authcodeExchange.wantGrantedScopes)

			// Check that the access token and refresh token storage were both deleted, and the number of other storage objects did not change.
			testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{crud.SecretLabelKey: authorizationcode.TypeLabelValue}, 1)
			testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{crud.SecretLabelKey: openidconnect.TypeLabelValue}, 1)
			testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{crud.SecretLabelKey: accesstoken.TypeLabelValue}, 0)
			testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{crud.SecretLabelKey: refreshtoken.TypeLabelValue}, 0)
			testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{crud.SecretLabelKey: storagepkce.TypeLabelValue}, 0)
			testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{}, 2)
		})
	}
}

func TestTokenExchange(t *testing.T) {
	// TODO write this test
	t.Skip()
	tests := []struct {
		name                       string
		authcodeExchange           authcodeExchangeInputs
		wantStatus                 int
		requestedAudience          string
		modifyTokenExchangeRequest func(r *http.Request)
	}{
		{
			name: "token exchange happy path",
			authcodeExchange: authcodeExchangeInputs{
				modifyAuthRequest: func(authRequest *http.Request) {
					authRequest.Form.Set("scope", "openid pinniped.sts.unrestricted")
				},
				wantStatus:          http.StatusOK,
				wantBodyFields:      []string{"id_token", "access_token", "token_type", "expires_in", "scope"},
				wantRequestedScopes: []string{"openid", "pinniped.sts.unrestricted"},
			},
			wantStatus: http.StatusOK,
		},
	}
	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			subject, rsp, _, _, _ := exchangeAuthcodeForTokens(t, test.authcodeExchange)
			var parsedResponseBody map[string]interface{}
			require.NoError(t, json.Unmarshal(rsp.Body.Bytes(), &parsedResponseBody))

			request := happyTokenExchangeRequest("foo-cluster", parsedResponseBody["access_token"].(string))

			req := httptest.NewRequest("POST", "/path/shouldn't/matter", body(request.Form).ReadCloser())
			req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
			if test.modifyTokenExchangeRequest != nil {
				test.modifyTokenExchangeRequest(req)
			}
			rsp = httptest.NewRecorder()

			subject.ServeHTTP(rsp, req)
			t.Logf("response: %#v", rsp)
			t.Logf("response body: %q", rsp.Body.String())

			require.Equal(t, test.wantStatus, rsp.Code)
			testutil.RequireEqualContentType(t, rsp.Header().Get("Content-Type"), "application/json")
		})
	}
}

func exchangeAuthcodeForTokens(t *testing.T, test authcodeExchangeInputs) (
	subject http.Handler,
	rsp *httptest.ResponseRecorder,
	authCode string,
	secrets v1.SecretInterface,
	oauthStore *oidc.KubeStorage,
) {
	authRequest := deepCopyRequestForm(happyAuthRequest)
	if test.modifyAuthRequest != nil {
		test.modifyAuthRequest(authRequest)
	}

	client := fake.NewSimpleClientset()
	secrets = client.CoreV1().Secrets("some-namespace")

	var oauthHelper fosite.OAuth2Provider
	var jwtSigningKey *ecdsa.PrivateKey

	oauthStore = oidc.NewKubeStorage(secrets)
	if test.makeOathHelper != nil {
		oauthHelper, authCode, jwtSigningKey = test.makeOathHelper(t, authRequest, oauthStore)
	} else {
		oauthHelper, authCode, jwtSigningKey = makeHappyOauthHelper(t, authRequest, oauthStore)
	}

	if test.modifyStorage != nil {
		test.modifyStorage(t, oauthStore, authCode)
	}
	subject = NewHandler(oauthHelper)

	authorizeEndpointGrantedOpenIDScope := strings.Contains(authRequest.Form.Get("scope"), "openid")
	expectedNumberOfIDSessionsStored := 0
	if authorizeEndpointGrantedOpenIDScope {
		expectedNumberOfIDSessionsStored = 1
	}

	testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{crud.SecretLabelKey: authorizationcode.TypeLabelValue}, 1)
	testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{crud.SecretLabelKey: storagepkce.TypeLabelValue}, 1)
	testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{crud.SecretLabelKey: openidconnect.TypeLabelValue}, expectedNumberOfIDSessionsStored)
	testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{}, 2+expectedNumberOfIDSessionsStored)

	req := httptest.NewRequest("POST", "/path/shouldn't/matter", happyBody(authCode).ReadCloser())
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if test.modifyTokenRequest != nil {
		test.modifyTokenRequest(req, authCode)
	}
	rsp = httptest.NewRecorder()

	subject.ServeHTTP(rsp, req)
	t.Logf("response: %#v", rsp)
	t.Logf("response body: %q", rsp.Body.String())

	testutil.RequireEqualContentType(t, rsp.Header().Get("Content-Type"), "application/json")
	require.Equal(t, test.wantStatus, rsp.Code)

	if test.wantStatus == http.StatusOK {
		require.NotNil(t, test.wantSuccessBodyFields, "problem with test table setup: wanted success but did not specify expected response body")

		var parsedResponseBody map[string]interface{}
		require.NoError(t, json.Unmarshal(rsp.Body.Bytes(), &parsedResponseBody))
		require.ElementsMatch(t, test.wantSuccessBodyFields, getMapKeys(parsedResponseBody))

		wantIDToken := contains(test.wantSuccessBodyFields, "id_token")
		wantRefreshToken := contains(test.wantSuccessBodyFields, "refresh_token")

		requireInvalidAuthCodeStorage(t, authCode, oauthStore)
		requireValidAccessTokenStorage(t, parsedResponseBody, oauthStore, test.wantRequestedScopes, test.wantGrantedScopes)
		requireInvalidPKCEStorage(t, authCode, oauthStore)
		requireValidOIDCStorage(t, parsedResponseBody, authCode, oauthStore, test.wantRequestedScopes, test.wantGrantedScopes)

		expectedNumberOfRefreshTokenSessionsStored := 0
		if wantRefreshToken {
			expectedNumberOfRefreshTokenSessionsStored = 1
		}
		expectedNumberOfIDSessionsStored = 0
		if wantIDToken {
			expectedNumberOfIDSessionsStored = 1
			requireValidIDToken(t, parsedResponseBody, jwtSigningKey)
		}
		if wantRefreshToken {
			requireValidRefreshTokenStorage(t, parsedResponseBody, oauthStore, test.wantRequestedScopes, test.wantGrantedScopes)
		}

		testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{crud.SecretLabelKey: authorizationcode.TypeLabelValue}, 1)
		testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{crud.SecretLabelKey: accesstoken.TypeLabelValue}, 1)
		testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{crud.SecretLabelKey: storagepkce.TypeLabelValue}, 0)
		testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{crud.SecretLabelKey: refreshtoken.TypeLabelValue}, expectedNumberOfRefreshTokenSessionsStored)
		testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{crud.SecretLabelKey: openidconnect.TypeLabelValue}, expectedNumberOfIDSessionsStored)
		testutil.RequireNumberOfSecretsMatchingLabelSelector(t, secrets, labels.Set{}, 2+expectedNumberOfRefreshTokenSessionsStored+expectedNumberOfIDSessionsStored)
	} else {
		require.NotNil(t, test.wantErrorResponseBody, "problem with test table setup: wanted failure but did not specify failure response body")

		require.JSONEq(t, test.wantErrorResponseBody, rsp.Body.String())
	}

	return subject, rsp, authCode, secrets, oauthStore
}

type body url.Values

func happyBody(happyAuthCode string) body {
	return map[string][]string{
		"grant_type":    {"authorization_code"},
		"code":          {happyAuthCode},
		"redirect_uri":  {goodRedirectURI},
		"code_verifier": {goodPKCECodeVerifier},
		"client_id":     {goodClient},
	}
}

func (b body) WithGrantType(grantType string) body {
	return b.with("grant_type", grantType)
}

func (b body) WithClientID(clientID string) body {
	return b.with("client_id", clientID)
}

func (b body) WithAuthCode(code string) body {
	return b.with("code", code)
}

func (b body) WithRedirectURI(redirectURI string) body {
	return b.with("redirect_uri", redirectURI)
}

func (b body) WithPKCE(verifier string) body {
	return b.with("code_verifier", verifier)
}

func (b body) ReadCloser() io.ReadCloser {
	return ioutil.NopCloser(strings.NewReader(url.Values(b).Encode()))
}

func (b body) with(param, value string) body {
	if value == "" {
		url.Values(b).Del(param)
	} else {
		url.Values(b).Set(param, value)
	}
	return b
}

// getFositeDataSignature returns the signature of the provided data. The provided data could be an auth code, access
// token, etc. It is assumed that the code is of the format "data.signature", which is how Fosite generates auth codes
// and access tokens.
func getFositeDataSignature(t *testing.T, data string) string {
	split := strings.Split(data, ".")
	require.Len(t, split, 2)
	return split[1]
}

func makeHappyOauthHelper(
	t *testing.T,
	authRequest *http.Request,
	store interface {
		oauth2.TokenRevocationStorage
		oauth2.CoreStorage
		openid.OpenIDConnectRequestStorage
		pkce.PKCERequestStorage
		fosite.ClientManager
	},
) (fosite.OAuth2Provider, string, *ecdsa.PrivateKey) {
	t.Helper()

	jwtSigningKey, jwkProvider := generateJWTSigningKeyAndJWKSProvider(t, goodIssuer)
	oauthHelper := oidc.FositeOauth2Helper(store, goodIssuer, []byte(hmacSecret), jwkProvider)
	authResponder := simulateAuthEndpointHavingAlreadyRun(t, authRequest, oauthHelper)
	return oauthHelper, authResponder.GetCode(), jwtSigningKey
}

func makeOauthHelperWithNilPrivateJWTSigningKey(
	t *testing.T,
	authRequest *http.Request,
	store interface {
		oauth2.TokenRevocationStorage
		oauth2.CoreStorage
		openid.OpenIDConnectRequestStorage
		pkce.PKCERequestStorage
		fosite.ClientManager
	},
) (fosite.OAuth2Provider, string, *ecdsa.PrivateKey) {
	t.Helper()

	jwkProvider := jwks.NewDynamicJWKSProvider() // empty provider which contains no signing key for this issuer
	oauthHelper := oidc.FositeOauth2Helper(store, goodIssuer, []byte(hmacSecret), jwkProvider)
	authResponder := simulateAuthEndpointHavingAlreadyRun(t, authRequest, oauthHelper)
	return oauthHelper, authResponder.GetCode(), nil
}

// Simulate the auth endpoint running so Fosite code will fill the store with realistic values.
func simulateAuthEndpointHavingAlreadyRun(t *testing.T, authRequest *http.Request, oauthHelper fosite.OAuth2Provider) fosite.AuthorizeResponder {
	// We only set the fields in the session that Fosite wants us to set.
	ctx := context.Background()
	session := &openid.DefaultSession{
		Claims: &jwt.IDTokenClaims{
			Subject:     goodSubject,
			AuthTime:    goodAuthTime,
			RequestedAt: goodRequestedAtTime,
		},
		Subject:  goodSubject,
		Username: goodUsername,
	}
	authRequester, err := oauthHelper.NewAuthorizeRequest(ctx, authRequest)
	require.NoError(t, err)
	if strings.Contains(authRequest.Form.Get("scope"), "openid") {
		authRequester.GrantScope("openid")
	}
	if strings.Contains(authRequest.Form.Get("scope"), "offline_access") {
		authRequester.GrantScope("offline_access")
	}
	if strings.Contains(authRequest.Form.Get("scope"), "pinniped.sts.unrestricted") {
		authRequester.GrantScope("pinniped.sts.unrestricted")
	}
	authResponder, err := oauthHelper.NewAuthorizeResponse(ctx, authRequester, session)
	require.NoError(t, err)
	return authResponder
}

func generateJWTSigningKeyAndJWKSProvider(t *testing.T, issuer string) (*ecdsa.PrivateKey, jwks.DynamicJWKSProvider) {
	t.Helper()

	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)

	jwksProvider := jwks.NewDynamicJWKSProvider()
	jwksProvider.SetIssuerToJWKSMap(
		nil, // public JWKS unused
		map[string]*jose.JSONWebKey{
			issuer: {Key: key},
		},
	)

	return key, jwksProvider
}

func requireInvalidAuthCodeStorage(
	t *testing.T,
	code string,
	storage oauth2.CoreStorage,
) {
	t.Helper()

	// Make sure we have invalidated this auth code.
	_, err := storage.GetAuthorizeCodeSession(context.Background(), getFositeDataSignature(t, code), nil)
	require.True(t, errors.Is(err, fosite.ErrInvalidatedAuthorizeCode))
}

func requireValidRefreshTokenStorage(
	t *testing.T,
	body map[string]interface{},
	storage oauth2.CoreStorage,
	wantRequestedScopes []string,
	wantGrantedScopes []string,
) {
	t.Helper()

	// Get the refresh token, and make sure we can use it to perform a lookup on the storage.
	refreshToken, ok := body["refresh_token"]
	require.True(t, ok)
	refreshTokenString, ok := refreshToken.(string)
	require.Truef(t, ok, "wanted refresh_token to be a string, but got %T", refreshToken)
	require.NotEmpty(t, refreshTokenString)
	storedRequest, err := storage.GetRefreshTokenSession(context.Background(), getFositeDataSignature(t, refreshTokenString), nil)
	require.NoError(t, err)

	// Fosite stores refresh tokens without any of the original request form parameters.
	requireValidStoredRequest(
		t,
		storedRequest,
		storedRequest.Sanitize([]string{}).GetRequestForm(),
		wantRequestedScopes,
		wantGrantedScopes,
		true,
	)
}

func requireValidAccessTokenStorage(
	t *testing.T,
	body map[string]interface{},
	storage oauth2.CoreStorage,
	wantRequestedScopes []string,
	wantGrantedScopes []string,
) {
	t.Helper()

	// Get the access token, and make sure we can use it to perform a lookup on the storage.
	accessToken, ok := body["access_token"]
	require.True(t, ok)
	accessTokenString, ok := accessToken.(string)
	require.Truef(t, ok, "wanted access_token to be a string, but got %T", accessToken)
	require.NotEmpty(t, accessTokenString)
	storedRequest, err := storage.GetAccessTokenSession(context.Background(), getFositeDataSignature(t, accessTokenString), nil)
	require.NoError(t, err)

	// Make sure the other body fields are valid.
	tokenType, ok := body["token_type"]
	require.True(t, ok)
	tokenTypeString, ok := tokenType.(string)
	require.Truef(t, ok, "wanted token_type to be a string, but got %T", tokenType)
	require.Equal(t, "bearer", tokenTypeString)

	expiresIn, ok := body["expires_in"]
	require.True(t, ok)
	expiresInNumber, ok := expiresIn.(float64) // Go unmarshals JSON numbers to float64, see `go doc encoding/json`
	require.Truef(t, ok, "wanted expires_in to be an float64, but got %T", expiresIn)
	require.InDelta(t, accessTokenExpirationSeconds, expiresInNumber, timeComparisonFudgeSeconds)

	scopes, ok := body["scope"]
	require.True(t, ok)
	actualGrantedScopesString, ok := scopes.(string)
	require.Truef(t, ok, "wanted scopes to be an string, but got %T", scopes)
	require.Equal(t, strings.Join(wantGrantedScopes, " "), actualGrantedScopesString)

	// Fosite stores access tokens without any of the original request form parameters.
	requireValidStoredRequest(
		t,
		storedRequest,
		storedRequest.Sanitize([]string{}).GetRequestForm(),
		wantRequestedScopes,
		wantGrantedScopes,
		true,
	)
}

func requireInvalidAccessTokenStorage(
	t *testing.T,
	body map[string]interface{},
	storage oauth2.CoreStorage,
) {
	t.Helper()

	// Get the access token, and make sure we can use it to perform a lookup on the storage.
	accessToken, ok := body["access_token"]
	require.True(t, ok)
	accessTokenString, ok := accessToken.(string)
	require.Truef(t, ok, "wanted access_token to be a string, but got %T", accessToken)
	_, err := storage.GetAccessTokenSession(context.Background(), getFositeDataSignature(t, accessTokenString), nil)
	require.True(t, errors.Is(err, fosite.ErrNotFound))
}

func requireInvalidPKCEStorage(
	t *testing.T,
	code string,
	storage pkce.PKCERequestStorage,
) {
	t.Helper()

	// Make sure the PKCE session has been deleted. Note that Fosite stores PKCE codes using the auth code signature
	// as a key.
	_, err := storage.GetPKCERequestSession(context.Background(), getFositeDataSignature(t, code), nil)
	require.True(t, errors.Is(err, fosite.ErrNotFound))
}

func requireValidOIDCStorage(
	t *testing.T,
	body map[string]interface{},
	code string,
	storage openid.OpenIDConnectRequestStorage,
	wantRequestedScopes []string,
	wantGrantedScopes []string,
) {
	t.Helper()

	if contains(wantGrantedScopes, "openid") {
		// Make sure the OIDC session is still there. Note that Fosite stores OIDC sessions using the full auth code as a key.
		storedRequest, err := storage.GetOpenIDConnectSession(context.Background(), code, nil)
		require.NoError(t, err)

		// Fosite stores OIDC sessions with only the nonce in the original request form.
		accessToken, ok := body["access_token"]
		require.True(t, ok)
		accessTokenString, ok := accessToken.(string)
		require.Truef(t, ok, "wanted access_token to be a string, but got %T", accessToken)
		require.NotEmpty(t, accessTokenString)

		requireValidStoredRequest(
			t,
			storedRequest,
			storedRequest.Sanitize([]string{"nonce"}).GetRequestForm(),
			wantRequestedScopes,
			wantGrantedScopes,
			false,
		)
	} else {
		_, err := storage.GetOpenIDConnectSession(context.Background(), code, nil)
		require.True(t, errors.Is(err, fosite.ErrNotFound))
	}
}

func requireValidStoredRequest(
	t *testing.T,
	request fosite.Requester,
	wantRequestForm url.Values,
	wantRequestedScopes []string,
	wantGrantedScopes []string,
	wantAccessTokenExpiresAt bool,
) {
	t.Helper()

	// Assert that the getters on the request return what we think they should.
	require.NotEmpty(t, request.GetID())
	testutil.RequireTimeInDelta(t, request.GetRequestedAt(), time.Now().UTC(), timeComparisonFudgeSeconds*time.Second)
	require.Equal(t, goodClient, request.GetClient().GetID())
	require.Equal(t, fosite.Arguments(wantRequestedScopes), request.GetRequestedScopes())
	require.Equal(t, fosite.Arguments(wantGrantedScopes), request.GetGrantedScopes())
	require.Empty(t, request.GetRequestedAudience())
	require.Empty(t, request.GetGrantedAudience())
	require.Equal(t, wantRequestForm, request.GetRequestForm()) // Fosite stores access token request without form

	// Cast session to the type we think it should be.
	session, ok := request.GetSession().(*openid.DefaultSession)
	require.Truef(t, ok, "could not cast %T to %T", request.GetSession(), &openid.DefaultSession{})

	// Assert that the session claims are what we think they should be, but only if we are doing OIDC.
	if contains(wantGrantedScopes, "openid") {
		claims := session.Claims
		require.Empty(t, claims.JTI) // When claims.JTI is empty, Fosite will generate a UUID for this field.
		require.Equal(t, goodSubject, claims.Subject)

		// We are in charge of setting these fields. For the purpose of testing, we ensure that the
		// sentinel test value is set correctly.
		require.Equal(t, goodRequestedAtTime, claims.RequestedAt)
		require.Equal(t, goodAuthTime, claims.AuthTime)

		// These fields will all be given good defaults by fosite at runtime and we only need to use them
		// if we want to override the default behaviors. We currently don't need to override these defaults,
		// so they do not end up being stored. Fosite sets its defaults at runtime in openid.DefaultSession's
		// GenerateIDToken() method.
		require.Empty(t, claims.Issuer)
		require.Empty(t, claims.Audience)
		require.Empty(t, claims.Nonce)
		require.Zero(t, claims.ExpiresAt)
		require.Zero(t, claims.IssuedAt)

		// Fosite unconditionally overwrites claims.AccessTokenHash at runtime in openid.OpenIDConnectExplicitHandler's
		// PopulateTokenEndpointResponse() method, just before it calls the same GenerateIDToken() mentioned above,
		// so it does not end up saved in storage.
		require.Empty(t, claims.AccessTokenHash)

		// At this time, we don't use any of these optional (per the OIDC spec) fields.
		require.Empty(t, claims.AuthenticationContextClassReference)
		require.Empty(t, claims.AuthenticationMethodsReference)
		require.Empty(t, claims.CodeHash)
		require.Empty(t, claims.Extra)
	}

	// Assert that the session headers are what we think they should be.
	headers := session.Headers
	require.Empty(t, headers)

	// Assert that the token expirations are what we think they should be.
	authCodeExpiresAt, ok := session.ExpiresAt[fosite.AuthorizeCode]
	require.True(t, ok, "expected session to hold expiration time for auth code")
	testutil.RequireTimeInDelta(
		t,
		time.Now().UTC().Add(authCodeExpirationSeconds*time.Second),
		authCodeExpiresAt,
		timeComparisonFudgeSeconds*time.Second,
	)

	// OpenID Connect sessions do not store access token expiration information.
	accessTokenExpiresAt, ok := session.ExpiresAt[fosite.AccessToken]
	if wantAccessTokenExpiresAt {
		require.True(t, ok, "expected session to hold expiration time for access token")
		testutil.RequireTimeInDelta(
			t,
			time.Now().UTC().Add(accessTokenExpirationSeconds*time.Second),
			accessTokenExpiresAt,
			timeComparisonFudgeSeconds*time.Second,
		)
	} else {
		require.False(t, ok, "expected session to not hold expiration time for access token, but it did")
	}

	// Assert that the session's username and subject are correct.
	require.Equal(t, goodUsername, session.Username)
	require.Equal(t, goodSubject, session.Subject)
}

func requireValidIDToken(t *testing.T, body map[string]interface{}, jwtSigningKey *ecdsa.PrivateKey) {
	t.Helper()

	idToken, ok := body["id_token"]
	require.Truef(t, ok, "body did not contain 'id_token': %s", body)
	idTokenString, ok := idToken.(string)
	require.Truef(t, ok, "wanted id_token to be a string, but got %T", idToken)

	// The go-oidc library will validate the signature and the client claim in the ID token.
	token := oidctestutil.VerifyECDSAIDToken(t, goodIssuer, goodClient, jwtSigningKey, idTokenString)

	var claims struct {
		Subject         string   `json:"sub"`
		Audience        []string `json:"aud"`
		Issuer          string   `json:"iss"`
		JTI             string   `json:"jti"`
		Nonce           string   `json:"nonce"`
		AccessTokenHash string   `json:"at_hash"`
		ExpiresAt       int64    `json:"exp"`
		IssuedAt        int64    `json:"iat"`
		RequestedAt     int64    `json:"rat"`
		AuthTime        int64    `json:"auth_time"`
	}

	// Note that there is a bug in fosite which prevents the `at_hash` claim from appearing in this ID token.
	// We can add a workaround for this later.
	idTokenFields := []string{"sub", "aud", "iss", "jti", "nonce", "auth_time", "exp", "iat", "rat"}

	// make sure that these are the only fields in the token
	var m map[string]interface{}
	require.NoError(t, token.Claims(&m))
	require.ElementsMatch(t, idTokenFields, getMapKeys(m))

	// verify each of the claims
	err := token.Claims(&claims)
	require.NoError(t, err)
	require.Equal(t, goodSubject, claims.Subject)
	require.Len(t, claims.Audience, 1)
	require.Equal(t, goodClient, claims.Audience[0])
	require.Equal(t, goodIssuer, claims.Issuer)
	require.NotEmpty(t, claims.JTI)
	require.Equal(t, goodNonce, claims.Nonce)

	expiresAt := time.Unix(claims.ExpiresAt, 0)
	issuedAt := time.Unix(claims.IssuedAt, 0)
	requestedAt := time.Unix(claims.RequestedAt, 0)
	authTime := time.Unix(claims.AuthTime, 0)
	testutil.RequireTimeInDelta(t, time.Now().UTC().Add(idTokenExpirationSeconds*time.Second), expiresAt, timeComparisonFudgeSeconds*time.Second)
	testutil.RequireTimeInDelta(t, time.Now().UTC(), issuedAt, timeComparisonFudgeSeconds*time.Second)
	testutil.RequireTimeInDelta(t, goodRequestedAtTime, requestedAt, timeComparisonFudgeSeconds*time.Second)
	testutil.RequireTimeInDelta(t, goodAuthTime, authTime, timeComparisonFudgeSeconds*time.Second)
}

func deepCopyRequestForm(r *http.Request) *http.Request {
	copied := url.Values{}
	for k, v := range r.Form {
		copied[k] = v
	}
	return &http.Request{Form: copied}
}

func getMapKeys(m map[string]interface{}) []string {
	keys := make([]string, 0)
	for key := range m {
		keys = append(keys, key)
	}
	return keys
}

func contains(haystack []string, needle string) bool {
	for _, hay := range haystack {
		if hay == needle {
			return true
		}
	}
	return false
}
