package executor

import (
	"net/http"
	"strings"
	"testing"
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
