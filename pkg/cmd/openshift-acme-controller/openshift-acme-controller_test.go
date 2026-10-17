package openshiftacmecontroller

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/tnozicka/openshift-acme/pkg/cmd/genericclioptions"
	"k8s.io/client-go/kubernetes/fake"
	coordinationv1 "k8s.io/client-go/kubernetes/typed/coordination/v1"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"
)

func init() {
	klog.InitFlags(nil)
}

func TestRun_ResourceLockError(t *testing.T) {
	var capturedLockType, capturedNs, capturedName string
	mockNewResourceLock := func(lockType string, ns string, name string, coreClient corev1.CoreV1Interface, coordinationClient coordinationv1.CoordinationV1Interface, rlc resourcelock.ResourceLockConfig) (resourcelock.Interface, error) {
		capturedLockType = lockType
		capturedNs = ns
		capturedName = name
		return nil, fmt.Errorf("injected mock error")
	}

	fakeClient := fake.NewSimpleClientset()
	opts := &Options{
		kubeClient:                  fakeClient,
		ControllerNamespace:         "default",
		LeaderelectionLeaseDuration: 60 * time.Second,
		LeaderelectionRenewDeadline: 35 * time.Second,
		LeaderelectionRetryPeriod:   10 * time.Second,
		NewResourceLock:             mockNewResourceLock,
	}

	err := opts.Run(&cobra.Command{}, genericclioptions.IOStreams{})
	if err == nil {
		t.Fatalf("expected error from resourcelock.New, got nil")
	}

	if capturedLockType != resourcelock.LeasesResourceLock {
		t.Errorf("expected lockType %q, got %q", resourcelock.LeasesResourceLock, capturedLockType)
	}
	if capturedNs != opts.ControllerNamespace {
		t.Errorf("expected ns %q, got %q", opts.ControllerNamespace, capturedNs)
	}
	if capturedName != "acme-controller-locks" {
		t.Errorf("expected name %q, got %q", "acme-controller-locks", capturedName)
	}

	expectedPrefix := "can't create resource lock: injected mock error"
	if !strings.HasPrefix(err.Error(), expectedPrefix) {
		t.Errorf("expected error to start with %q, got: %v", expectedPrefix, err)
	}
}

func TestOptionsDefaultsAndOverrides(t *testing.T) {
	t.Run("uses DefaultExposerImage by default", func(t *testing.T) {
		opts := NewOptions(genericclioptions.IOStreams{})
		if opts.ExposerImage != DefaultExposerImage {
			t.Errorf("expected default ExposerImage %q, got %q", DefaultExposerImage, opts.ExposerImage)
		}
		if opts.CertDefaultProfile != "" {
			t.Errorf("expected empty default CertDefaultProfile, got %q", opts.CertDefaultProfile)
		}
		if err := opts.Validate(); err != nil {
			t.Errorf("expected Validate() to succeed with DefaultExposerImage, got: %v", err)
		}
	})

	t.Run("supports CLI flag and env var overrides for exposer-image and cert-default-profile", func(t *testing.T) {
		t.Setenv("OPENSHIFT_ACME_CONTROLLER_CERT_DEFAULT_PROFILE", "tlsserver")
		t.Setenv("OPENSHIFT_ACME_EXPOSER_IMAGE", "custom-registry.example.com/exposer:v1")

		cmd := NewOpenshiftAcmeControllerCommand(genericclioptions.IOStreams{})
		cmd.RunE = func(c *cobra.Command, args []string) error {
			return nil
		}
		cmd.SetArgs([]string{})
		if err := cmd.Execute(); err != nil {
			t.Fatalf("unexpected error executing command: %v", err)
		}

		profileFlag := cmd.Flags().Lookup("cert-default-profile")
		if profileFlag == nil || profileFlag.Value.String() != "tlsserver" {
			t.Errorf("expected cert-default-profile flag to be populated from env var as %q, got %v", "tlsserver", profileFlag)
		}
		exposerFlag := cmd.Flags().Lookup("exposer-image")
		if exposerFlag == nil || exposerFlag.Value.String() != "custom-registry.example.com/exposer:v1" {
			t.Errorf("expected exposer-image flag to be populated from OPENSHIFT_ACME_EXPOSER_IMAGE, got %v", exposerFlag)
		}
	})
}
