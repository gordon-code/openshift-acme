package openshiftacmecontroller

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
	"github.com/tnozicka/openshift-acme/pkg/cmd/genericclioptions"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	corev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	coordinationv1 "k8s.io/client-go/kubernetes/typed/coordination/v1"
)

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
		kubeClient: fakeClient,
		ControllerNamespace: "default",
		LeaderelectionLeaseDuration: 60 * time.Second,
		LeaderelectionRenewDeadline: 35 * time.Second,
		LeaderelectionRetryPeriod:   10 * time.Second,
		NewResourceLock: mockNewResourceLock,
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
