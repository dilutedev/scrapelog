package httpapi

import "net/http"

func withNoStore(next http.HandlerFunc) http.HandlerFunc {
	return func(responseWriter http.ResponseWriter, request *http.Request) {
		responseWriter.Header().Set("Cache-Control", "no-store")
		next(responseWriter, request)
	}
}
