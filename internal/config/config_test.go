package config

import (
	"strings"
	"testing"
)

func TestTelegramTokenComesFromTheEnvironment(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "a-long-test-password")
	t.Setenv("TELEGRAM_BOT_TOKEN", "  123456789:AAexampleexampleexampleexample  ")
	c, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if c.TelegramBotToken != "123456789:AAexampleexampleexampleexample" {
		t.Fatalf("token = %q", c.TelegramBotToken)
	}
	if got := c.LogValue().String(); strings.Contains(got, "AAexample") {
		t.Fatalf("the token is in the log value: %s", got)
	}
}

func TestAdminPasswordIsRequired(t *testing.T) {
	t.Setenv("ADMIN_PASSWORD", "")
	if _, err := Load(); err == nil {
		t.Fatal("Load accepted an empty admin password")
	}
}
