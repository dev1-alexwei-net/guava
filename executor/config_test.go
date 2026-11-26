package executor

import (
	"net/http"
	"strings"
	"testing"

	"github.com/dev1-alexwei-net/guava/conversations"
)

func validConfig() Config {
	return Config{
		DatabasePath:     "/tmp/fileexplorer.sqlite",
		RootPath:         "/tmp/files",
		InstructionsPath: "/tmp/instructions.md",
		GatewayURL:       "http://127.0.0.1:18789/",
		FileExplorerURL:  "http://127.0.0.1:8080/",
		AgentID:          "default",
	}
}

func TestConfigValidateRequiresInputs(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "database", mutate: func(c *Config) { c.DatabasePath = " " }, want: "database path"},
		{name: "root", mutate: func(c *Config) { c.RootPath = "" }, want: "filesystem root"},
		{name: "instructions", mutate: func(c *Config) { c.InstructionsPath = "" }, want: "instructions path"},
		{name: "Gateway", mutate: func(c *Config) { c.GatewayURL = "" }, want: "Gateway URL"},
		{name: "fileexplorer", mutate: func(c *Config) { c.FileExplorerURL = " " }, want: "fileexplorer URL"},
		{name: "agent", mutate: func(c *Config) { c.AgentID = "" }, want: "agent ID"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			test.mutate(&config)
			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want it to contain %q", err, test.want)
			}
		})
	}
}

func TestConfigValidateGatewayURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "relative", url: "/gateway", want: "http or https"},
		{name: "unsupported scheme", url: "ftp://gateway.example", want: "http or https"},
		{name: "missing host", url: "https:///path", want: "host"},
		{name: "userinfo", url: "https://user:pass@gateway.example", want: "user information"},
		{name: "query", url: "https://gateway.example?x=1", want: "query or fragment"},
		{name: "fragment", url: "https://gateway.example#part", want: "query or fragment"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			config.GatewayURL = test.url
			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want it to contain %q", err, test.want)
			}
		})
	}

	t.Run("valid HTTP and HTTPS URLs", func(t *testing.T) {
		for _, gatewayURL := range []string{
			"http://127.0.0.1:18789",
			"https://gateway.example/openclaw/",
		} {
			config := validConfig()
			config.GatewayURL = gatewayURL
			if err := config.Validate(); err != nil {
				t.Errorf("Validate(%q) error = %v", gatewayURL, err)
			}
		}
	})
}

func TestConfigValidateFileExplorerURL(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want string
	}{
		{name: "relative", url: "/fileexplorer", want: "http or https"},
		{name: "unsupported scheme", url: "ftp://fileexplorer.example", want: "http or https"},
		{name: "missing host", url: "https:///files", want: "host"},
		{name: "userinfo", url: "https://user:pass@fileexplorer.example", want: "user information"},
		{name: "query", url: "https://fileexplorer.example?x=1", want: "query or fragment"},
		{name: "fragment", url: "https://fileexplorer.example#files", want: "query or fragment"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			config.FileExplorerURL = test.url
			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want it to contain %q", err, test.want)
			}
		})
	}

	t.Run("valid HTTP and HTTPS URLs", func(t *testing.T) {
		for _, fileExplorerURL := range []string{
			"http://127.0.0.1:8080",
			"https://fileexplorer.container:8443/explorer/",
		} {
			config := validConfig()
			config.FileExplorerURL = fileExplorerURL
			if err := config.Validate(); err != nil {
				t.Errorf("Validate(%q) error = %v", fileExplorerURL, err)
			}
		}
	})
}

func TestConfigValidateSubdirectory(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*Config)
		want   string
	}{
		{name: "absolute path", mutate: func(c *Config) { c.Subdirectory = "/absolute" }, want: "inside the filesystem root"},
		{name: "escapes root", mutate: func(c *Config) { c.Subdirectory = "../escape" }, want: "inside the filesystem root"},
		{name: "nested escape", mutate: func(c *Config) { c.Subdirectory = "a/../../escape" }, want: "inside the filesystem root"},
		{name: "root itself", mutate: func(c *Config) { c.Subdirectory = "." }, want: "inside the filesystem root"},
		{name: "NUL byte", mutate: func(c *Config) { c.Subdirectory = "docs\x00" }, want: "NUL bytes"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := validConfig()
			test.mutate(&config)
			err := config.Validate()
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("Validate() error = %v, want it to contain %q", err, test.want)
			}
		})
	}

	t.Run("valid subdirectories", func(t *testing.T) {
		for _, subdirectory := range []string{"", "  ", "docs", "nested/folder", "docs/", "a/b/c"} {
			config := validConfig()
			config.Subdirectory = subdirectory
			if err := config.Validate(); err != nil {
				t.Errorf("Validate(%q) error = %v", subdirectory, err)
			}
		}
	})
}

func TestNewValidatesAndNormalizesConfig(t *testing.T) {
	config := validConfig()
	config.GatewayURL = " http://127.0.0.1:18789/// "
	config.FileExplorerURL = " http://fileexplorer.container:8080/// "
	config.Subdirectory = " docs/nested/// "

	executor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := executor.config.GatewayURL, "http://127.0.0.1:18789"; got != want {
		t.Errorf("GatewayURL = %q, want %q", got, want)
	}
	if got, want := executor.config.FileExplorerURL, "http://fileexplorer.container:8080"; got != want {
		t.Errorf("FileExplorerURL = %q, want %q", got, want)
	}
	if got, want := executor.config.Subdirectory, "docs/nested"; got != want {
		t.Errorf("Subdirectory = %q, want %q", got, want)
	}
	if executor.config.HTTPClient != http.DefaultClient {
		t.Errorf("HTTPClient = %p, want default client %p", executor.config.HTTPClient, http.DefaultClient)
	}

	config = validConfig()
	executor, err = New(config)
	if err != nil {
		t.Fatal(err)
	}
	if executor.config.Subdirectory != "" {
		t.Errorf("Subdirectory = %q, want empty for whole-root behavior", executor.config.Subdirectory)
	}
}

func TestNewKeepsCustomHTTPClient(t *testing.T) {
	config := validConfig()
	customClient := &http.Client{}
	config.HTTPClient = customClient

	executor, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	if executor.config.HTTPClient != customClient {
		t.Fatal("New did not preserve the configured HTTP client")
	}
}

func TestWithSubdirectoryDerivesScopedExecutor(t *testing.T) {
	base := validConfig()
	base.CLIArgs = `executor -database /var/lib/fileexplorer.sqlite -root /srv/files`
	original, err := New(base)
	if err != nil {
		t.Fatal(err)
	}

	detected, err := original.WithSubdirectory("docs")
	if err != nil {
		t.Fatal(err)
	}

	if detected.config.Subdirectory != "docs" {
		t.Errorf("derived Subdirectory = %q, want %q", detected.config.Subdirectory, "docs")
	}
	if detected.config.DatabasePath != base.DatabasePath || detected.config.RootPath != base.RootPath {
		t.Errorf("derived database/root = %q/%q, want the base %q/%q preserved",
			detected.config.DatabasePath, detected.config.RootPath, base.DatabasePath, base.RootPath)
	}
	if original.config.Subdirectory != "" {
		t.Errorf("original Subdirectory = %q, want the base unchanged after derivation", original.config.Subdirectory)
	}
	if original.config.CLIArgs != base.CLIArgs {
		t.Errorf("original CLIArgs = %q, want %q unchanged", original.config.CLIArgs, base.CLIArgs)
	}
	wantArgs := base.CLIArgs + " -subdirectory docs"
	if detected.config.CLIArgs != wantArgs {
		t.Errorf("derived CLIArgs = %q, want %q", detected.config.CLIArgs, wantArgs)
	}
	if status := detected.SessionSummary().Status; status != conversations.StatusIdle {
		t.Errorf("derived status = %q, want idle before a run", status)
	}
}

func TestWithSubdirectoryOverridesPreviousSelection(t *testing.T) {
	base := validConfig()
	base.Subdirectory = "docs"
	original, err := New(base)
	if err != nil {
		t.Fatal(err)
	}

	detected, err := original.WithSubdirectory("docs/deep")
	if err != nil {
		t.Fatal(err)
	}
	if detected.config.Subdirectory != "docs/deep" {
		t.Errorf("derived Subdirectory = %q, want the newer selection %q", detected.config.Subdirectory, "docs/deep")
	}
	if original.config.Subdirectory != "docs" {
		t.Errorf("original Subdirectory = %q, want the previous selection %q preserved", original.config.Subdirectory, "docs")
	}
}

func TestWithSubdirectoryNormalizesSelection(t *testing.T) {
	original, err := New(validConfig())
	if err != nil {
		t.Fatal(err)
	}

	detected, err := original.WithSubdirectory(" docs/nested/// ")
	if err != nil {
		t.Fatal(err)
	}
	if detected.config.Subdirectory != "docs/nested" {
		t.Errorf("derived Subdirectory = %q, want normalized %q", detected.config.Subdirectory, "docs/nested")
	}
}

func TestWithSubdirectoryEmptyKeepsWholeRoot(t *testing.T) {
	base := validConfig()
	base.Subdirectory = "docs"
	base.CLIArgs = "executor -root /srv/files"
	original, err := New(base)
	if err != nil {
		t.Fatal(err)
	}

	detected, err := original.WithSubdirectory("")
	if err != nil {
		t.Fatal(err)
	}
	if detected.config.Subdirectory != "" {
		t.Errorf("derived Subdirectory = %q, want empty for the whole root", detected.config.Subdirectory)
	}
	if detected.config.CLIArgs != base.CLIArgs {
		t.Errorf("derived CLIArgs = %q, want %q unchanged when unscoping", detected.config.CLIArgs, base.CLIArgs)
	}
}

func TestWithSubdirectoryRejectsInvalidSelection(t *testing.T) {
	original, err := New(validConfig())
	if err != nil {
		t.Fatal(err)
	}

	for _, selection := range []string{"/absolute", "../escape", "a/../../escape", "."} {
		if _, err := original.WithSubdirectory(selection); err == nil {
			t.Errorf("WithSubdirectory(%q) succeeded, want an error for a folder outside the root", selection)
		}
	}
	if original.config.Subdirectory != "" {
		t.Errorf("original Subdirectory = %q, want unchanged after failed derivations", original.config.Subdirectory)
	}
}

func TestWithSubdirectoryNilExecutor(t *testing.T) {
	var executor *Executor
	if _, err := executor.WithSubdirectory("docs"); err == nil {
		t.Error("WithSubdirectory on a nil executor succeeded, want an error")
	}
}
