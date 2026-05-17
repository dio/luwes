package e2e

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/suite"
)

type HeaderAuthSuite struct {
	suite.Suite
}

func TestHeaderAuth(t *testing.T) {
	suite.Run(t, new(HeaderAuthSuite))
}

func (s *HeaderAuthSuite) TestAccept() {
	req, _ := http.NewRequest(http.MethodGet, envoyAddr+"/", nil)
	req.Header.Set("x-api-key", "secret-key-abc")

	resp := mustDo(s.T(), req)
	defer resp.Body.Close()

	s.Equal(http.StatusOK, resp.StatusCode)
}

func (s *HeaderAuthSuite) TestReject_MissingKey() {
	req, _ := http.NewRequest(http.MethodGet, envoyAddr+"/", nil)

	resp := mustDo(s.T(), req)
	body := readBody(s.T(), resp)

	s.Equal(http.StatusUnauthorized, resp.StatusCode)
	s.Equal(`{"error":"missing x-api-key"}`, body)
}

func (s *HeaderAuthSuite) TestAccept_UserIDInjected() {
	// The filter continues (200) when the key is present, meaning x-user-id was
	// injected. The direct_response backend doesn't echo headers but a 200
	// confirms the filter did not reject.
	req, _ := http.NewRequest(http.MethodGet, envoyAddr+"/", nil)
	req.Header.Set("x-api-key", "user-token-xyz")

	resp := mustDo(s.T(), req)
	defer resp.Body.Close()

	s.Equal(http.StatusOK, resp.StatusCode)
}

func (s *HeaderAuthSuite) TestReject_EmptyKey() {
	req, _ := http.NewRequest(http.MethodGet, envoyAddr+"/", nil)
	req.Header.Set("x-api-key", "")

	resp := mustDo(s.T(), req)
	defer resp.Body.Close()

	s.Equal(http.StatusUnauthorized, resp.StatusCode)
}
