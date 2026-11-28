// Package web contains small Gin helpers shared by Guava's HTTP services.
package web

import (
	"mime"

	"github.com/gin-gonic/gin"
)

// SecurityHeaders adds the shared content-sniffing protection header to every
// response handled by the engine.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Next()
	}
}

// FormValue reads a request form value using net/http's form parsing and
// precedence rules, independent of Gin's route and rendering APIs.
func FormValue(c *gin.Context, name string) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return c.Request.FormValue(name)
}

// QueryValue returns the first value for name in the request URL query.
func QueryValue(c *gin.Context, name string) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return ""
	}
	return c.Request.URL.Query().Get(name)
}

// HeaderValue returns the request header value using net/http's canonical,
// case-insensitive header lookup.
func HeaderValue(c *gin.Context, name string) string {
	if c == nil || c.Request == nil {
		return ""
	}
	return c.Request.Header.Get(name)
}

// ParseMediaTypeHeader parses a MIME media type header, including its
// parameters. It returns the same errors as mime.ParseMediaType.
func ParseMediaTypeHeader(c *gin.Context, name string) (string, map[string]string, error) {
	return mime.ParseMediaType(HeaderValue(c, name))
}
