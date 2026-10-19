// Package util provides shared CLI helper functions for the openshift-acme
// commands.
package util

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/util/errors"
	"k8s.io/klog/v2"
)

const (
	FlagLogLevelKey = "loglevel"
)

func UsageError(cmd *cobra.Command, format string, args ...interface{}) error {
	msg := fmt.Sprintf(format, args...)
	return fmt.Errorf("%s\nSee '%s -h' for help and examples", msg, cmd.CommandPath())
}

func NormalizeNameForEnvVar(name string) string {
	s := strings.ToUpper(name)
	s = strings.ReplaceAll(s, "-", "_")
	return s
}

func ReadFlagsFromEnv(prefix string, cmd *cobra.Command) error {
	var errs []error
	flags := cmd.Flags()
	environ := os.Environ()
	sort.Strings(environ)

	for _, entry := range environ {
		envVarName, v, ok := strings.Cut(entry, "=")
		if !ok || !strings.HasPrefix(envVarName, prefix) {
			continue
		}

		rawName := strings.ToLower(strings.TrimPrefix(envVarName, prefix))
		f := flags.Lookup(strings.ReplaceAll(rawName, "_", "-"))
		if f == nil {
			f = flags.Lookup(rawName)
		}
		if f == nil || NormalizeNameForEnvVar(prefix+f.Name) != envVarName {
			continue
		}

		if f.Changed {
			// flags always take precedence over environment
			continue
		}

		err := f.Value.Set(v)
		if err != nil {
			errs = append(errs, fmt.Errorf("can't parse env var %q with value %q into flag %q: %v", envVarName, v, f.Name, err))
			continue
		}

		f.Changed = true
	}

	return errors.NewAggregate(errs)
}

func InstallKlog(cmd *cobra.Command) {
	level := flag.CommandLine.Lookup("v").Value.(*klog.Level)
	levelPtr := (*int32)(level)
	cmd.PersistentFlags().Int32Var(levelPtr, FlagLogLevelKey, *levelPtr, "Set the level of log output (0-10)")
	if cmd.PersistentFlags().Lookup("v") == nil {
		cmd.PersistentFlags().Int32Var(levelPtr, "v", *levelPtr, "Set the level of log output (0-10)")
	}
	cmd.PersistentFlags().Lookup("v").Hidden = true
}

func GetLoglevel() string {
	f := flag.CommandLine.Lookup("v")
	if f != nil {
		return f.Value.String()
	}

	return ""
}
