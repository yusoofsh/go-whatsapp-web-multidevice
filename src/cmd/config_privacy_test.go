package cmd

import (
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/spf13/viper"
)

// Isolate configuration globals. Only config loading runs, never app startup.
func TestEnvironmentConfigDoesNotLogCredentials(t *testing.T) {
	const marker = "gowa-regression-secret-never-log"
	const child = "GOWA_CONFIG_PRIVACY_TEST_CHILD"
	if os.Getenv(child) == "1" {
		viper.Set("app_basic_auth", "fixture:"+marker)
		viper.Set("whatsapp_webhook_secret", marker)
		viper.Set("chatwoot_api_token", marker)
		initEnvConfig()
		return
	}
	command := exec.Command(os.Args[0], "-test.run=^TestEnvironmentConfigDoesNotLogCredentials$")
	command.Dir = t.TempDir()
	command.Env = append(os.Environ(), child+"=1")
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated config test failed: %v", err)
	}
	if strings.Contains(string(output), marker) {
		t.Fatal("configuration credentials were emitted to stdout or stderr")
	}
}
