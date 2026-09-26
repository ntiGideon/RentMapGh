package auth

import (
	"net/http"
	"net/http/httptest"
)

func newRecorder() *httptest.ResponseRecorder { return httptest.NewRecorder() }

// requestWith builds a request carrying the cookies rec set.
func requestWith(rec *httptest.ResponseRecorder) *http.Request {
	req := httptest.NewRequest(http.MethodGet, "/login/verify", nil)
	for _, c := range rec.Result().Cookies() {
		req.AddCookie(c)
	}
	return req
}
