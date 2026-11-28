// Package web contains Gin middleware shared by Guava's HTTP services.
package web

import "github.com/gin-gonic/gin"

// SecurityHeaders adds the shared content-sniffing protection header to every
// response handled by the engine.
func SecurityHeaders() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("X-Content-Type-Options", "nosniff")
		c.Next()
	}
}
