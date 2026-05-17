package e2e

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/suite"
)

// HeaderAuthSuite tests the raw luwes SDK filter on port 10000.
type HeaderAuthSuite struct {
	suite.Suite
}

func TestHeaderAuth(t *testing.T) {
	suite.Run(t, new(HeaderAuthSuite))
}

func (s *HeaderAuthSuite) TestAccept() {
	req, _ := http.NewRequest(http.MethodGet, headerAuthAddr+"/", nil)
	req.Header.Set("x-api-key", "secret-key-abc")
	resp := mustDo(s.T(), req)
	defer resp.Body.Close()
	s.Equal(http.StatusOK, resp.StatusCode)
}

func (s *HeaderAuthSuite) TestReject_MissingKey() {
	req, _ := http.NewRequest(http.MethodGet, headerAuthAddr+"/", nil)
	resp := mustDo(s.T(), req)
	body := readBody(s.T(), resp)
	s.Equal(http.StatusUnauthorized, resp.StatusCode)
	s.Equal(`{"error":"missing x-api-key"}`, body)
}

func (s *HeaderAuthSuite) TestAccept_UserIDInjected() {
	req, _ := http.NewRequest(http.MethodGet, headerAuthAddr+"/", nil)
	req.Header.Set("x-api-key", "user-token-xyz")
	resp := mustDo(s.T(), req)
	defer resp.Body.Close()
	s.Equal(http.StatusOK, resp.StatusCode)
}

func (s *HeaderAuthSuite) TestReject_EmptyKey() {
	req, _ := http.NewRequest(http.MethodGet, headerAuthAddr+"/", nil)
	req.Header.Set("x-api-key", "")
	resp := mustDo(s.T(), req)
	defer resp.Body.Close()
	s.Equal(http.StatusUnauthorized, resp.StatusCode)
}

// HeaderAuthSahlSuite tests the sahl ergonomic layer on port 10001.
// Identical contract to HeaderAuthSuite: proves sahl behaves the same.
type HeaderAuthSahlSuite struct {
	suite.Suite
}

func TestHeaderAuthSahl(t *testing.T) {
	suite.Run(t, new(HeaderAuthSahlSuite))
}

func (s *HeaderAuthSahlSuite) TestAccept() {
	req, _ := http.NewRequest(http.MethodGet, headerAuthSahlAddr+"/", nil)
	req.Header.Set("x-api-key", "secret-key-abc")
	resp := mustDo(s.T(), req)
	defer resp.Body.Close()
	s.Equal(http.StatusOK, resp.StatusCode)
}

func (s *HeaderAuthSahlSuite) TestReject_MissingKey() {
	req, _ := http.NewRequest(http.MethodGet, headerAuthSahlAddr+"/", nil)
	resp := mustDo(s.T(), req)
	body := readBody(s.T(), resp)
	s.Equal(http.StatusUnauthorized, resp.StatusCode)
	s.Equal(`{"error":"missing x-api-key"}`, body)
}

func (s *HeaderAuthSahlSuite) TestAccept_UserIDInjected() {
	req, _ := http.NewRequest(http.MethodGet, headerAuthSahlAddr+"/", nil)
	req.Header.Set("x-api-key", "user-token-xyz")
	resp := mustDo(s.T(), req)
	defer resp.Body.Close()
	s.Equal(http.StatusOK, resp.StatusCode)
}

func (s *HeaderAuthSahlSuite) TestReject_EmptyKey() {
	req, _ := http.NewRequest(http.MethodGet, headerAuthSahlAddr+"/", nil)
	req.Header.Set("x-api-key", "")
	resp := mustDo(s.T(), req)
	defer resp.Body.Close()
	s.Equal(http.StatusUnauthorized, resp.StatusCode)
}
