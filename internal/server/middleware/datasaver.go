package middleware

import (
	"net/http"

	"rentmapgh/internal/server/reqctx"
)

// DataSaverCookie holds the user's explicit choice: "1" on, "0" off.
const DataSaverCookie = "ds"

// SetDataSaverCookie records an explicit choice for a year.
func SetDataSaverCookie(w http.ResponseWriter, on bool) {
	v := "0"
	if on {
		v = "1"
	}
	http.SetCookie(w, &http.Cookie{Name: DataSaverCookie, Value: v, Path: "/", MaxAge: 365 * 24 * 3600, SameSite: http.SameSiteLaxMode}) //nolint:gosec // G124: a UI preference read by JS too, not a credential
}

// DataSaver enables data-saver mode when the user turned it on, or when the
// browser sends `Save-Data: on` and the user hasn't opted out.
func DataSaver(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		on := r.Header.Get("Save-Data") == "on"
		if c, err := r.Cookie(DataSaverCookie); err == nil {
			on = c.Value == "1"
		}
		w.Header().Add("Vary", "Save-Data")
		next.ServeHTTP(w, r.WithContext(reqctx.WithDataSaver(r.Context(), on)))
	})
}
