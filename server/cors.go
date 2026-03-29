package server

import (
	"net/http"
	"regexp"
	"strings"
)

// allowedOriginPattern matches braintrust.dev and braintrustdata.dev origins
// including subdomains and preview deployments.
var allowedOriginPattern = regexp.MustCompile(`^https?://([\w-]+\.)*(braintrust|braintrustdata)\.dev$`)

var corsAllowHeaders = strings.Join([]string{
	"Content-Type",
	"Authorization",
	"X-Api-Key",
	"X-Bt-Auth-Token",
	"X-Bt-Parent",
	"X-Bt-Org-Name",
	"X-Bt-Project-Id",
	"X-Bt-Cursor",
	"X-Bt-Found-Existing-Experiment",
	"X-Bt-Span-Id",
	"X-Bt-Span-Export",
}, ", ")

const (
	corsAllowMethods = "GET, POST, OPTIONS"
	corsMaxAge       = "86400"
)

// corsMiddleware wraps an http.Handler with CORS support for Braintrust origins.
func corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && allowedOriginPattern.MatchString(origin) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Methods", corsAllowMethods)
			w.Header().Set("Access-Control-Allow-Headers", corsAllowHeaders)
			w.Header().Set("Access-Control-Expose-Headers", corsAllowHeaders)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Max-Age", corsMaxAge)
			// Support Chrome Private Network Access
			w.Header().Set("Access-Control-Allow-Private-Network", "true")
		}

		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}

		next.ServeHTTP(w, r)
	})
}
