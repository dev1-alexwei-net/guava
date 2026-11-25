// Package version holds the build-time semantic version of the binary.
package version

// SemVer is the semantic version of the published image the binary was
// built with (for example "1.0.3"). It is injected at build time with
//
//	-ldflags "-X github.com/dev1-alexwei-net/guava/internal/version.SemVer=1.0.3"
//
// so the web UIs can display it in their footer.
//
// Development builds are not passed a value and therefore display the "dev"
// default, which is clearly a development value rather than a release
// version.
var SemVer = "dev"
