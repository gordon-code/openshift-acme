package util

import (
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func TestReadFlagsFromEnv(t *testing.T) {
	t.Run("populates unset hyphenated and single-word flags from matching env vars", func(t *testing.T) {
		t.Setenv("TEST_PREFIX_WORKERS", "12")
		t.Setenv("TEST_PREFIX_CERT_DEFAULT_PROFILE", "shortlived")
		t.Setenv("TEST_PREFIX_UNREGISTERED_VAR", "ignored")
		t.Setenv("OTHER_PREFIX_WORKERS", "99")

		cmd := &cobra.Command{Use: "test"}
		workers := 50
		profile := ""
		cmd.Flags().IntVar(&workers, "workers", workers, "")
		cmd.Flags().StringVar(&profile, "cert-default-profile", profile, "")

		if err := ReadFlagsFromEnv("TEST_PREFIX_", cmd); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if workers != 12 {
			t.Errorf("expected workers=12, got %d", workers)
		}
		if profile != "shortlived" {
			t.Errorf("expected profile=%q, got %q", "shortlived", profile)
		}
		if !cmd.Flags().Lookup("workers").Changed {
			t.Errorf("expected workers flag to be marked Changed")
		}
	})

	t.Run("does not overwrite flags explicitly changed on CLI", func(t *testing.T) {
		t.Setenv("TEST_PREFIX_WORKERS", "12")

		cmd := &cobra.Command{Use: "test"}
		workers := 50
		cmd.Flags().IntVar(&workers, "workers", workers, "")
		if err := cmd.Flags().Set("workers", "25"); err != nil {
			t.Fatalf("unexpected error setting flag: %v", err)
		}

		if err := ReadFlagsFromEnv("TEST_PREFIX_", cmd); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if workers != 25 {
			t.Errorf("expected CLI value workers=25 to win over env var, got %d", workers)
		}
	})

	t.Run("returns aggregated parse error on invalid env var value", func(t *testing.T) {
		t.Setenv("TEST_PREFIX_WORKERS", "not-an-int")

		cmd := &cobra.Command{Use: "test"}
		workers := 50
		cmd.Flags().IntVar(&workers, "workers", workers, "")

		err := ReadFlagsFromEnv("TEST_PREFIX_", cmd)
		if err == nil || !strings.Contains(err.Error(), `can't parse env var "TEST_PREFIX_WORKERS"`) {
			t.Fatalf("expected parse error for TEST_PREFIX_WORKERS, got: %v", err)
		}
	})
}
