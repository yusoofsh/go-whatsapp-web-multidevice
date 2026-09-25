package oauth

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// AuthenticateHTTP reuses the existing OAuth store's expiry, audience and token
// checks. It returns a principal key, never a credential, for session binding.
func (s *Server) AuthenticateHTTP(ctx context.Context, header string, basic CredentialValidator) (string, error) {
	scheme, credentials := splitAuthorization(header)
	switch strings.ToLower(scheme) {
	case "bearer":
		p, err := s.store.validateAccessToken(ctx, credentials, s.resource.String(), s.now())
		if err != nil {
			return "", errors.New("invalid OAuth access token")
		}
		return fmt.Sprintf("oauth:%d:%s:%s", len(p.ClientID), p.ClientID, p.Subject), nil
	case "basic":
		username, password, ok := decodeBasicCredentials(credentials)
		if !ok || basic == nil || !basic(username, password) {
			return "", errors.New("invalid Basic credentials")
		}
		return "basic:" + username, nil
	default:
		return "", errors.New("authentication required")
	}
}
func (s *Server) HTTPChallenge() string {
	return fmt.Sprintf(`Bearer resource_metadata="%s", scope="%s"`, s.protectedResourceMetadataURL(), defaultScope)
}
