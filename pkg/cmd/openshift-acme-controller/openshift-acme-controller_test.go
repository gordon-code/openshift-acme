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
	origNewResourceLock := newResourceLock
	defer func() { newResourceLock = origNewResourceLock }()

	newResourceLock = func(lockType string, ns string, name string, coreClient corev1.CoreV1Interface, coordinationClient coordinationv1.CoordinationV1Interface, rlc resourcelock.ResourceLockConfig) (resourcelock.Interface, error) {
		return nil, fmt.Errorf("injected mock error")
	}

	fakeClient := fake.NewSimpleClientset()
	opts := &Options{
		kubeClient: fakeClient,
		ControllerNamespace: "default",
		LeaderelectionLeaseDuration: 60 * time.Second,
		LeaderelectionRenewDeadline: 35 * time.Second,
		LeaderelectionRetryPeriod:   10 * time.Second,
	}

	err := opts.Run(&cobra.Command{}, genericclioptions.IOStreams{})
	if err == nil {
		t.Fatalf("expected error from resourcelock.New, got nil")
	}

	expectedPrefix := "can't create resource lock: injected mock error"
	if !strings.HasPrefix(err.Error(), expectedPrefix) {
		t.Errorf("expected error to start with %q, got: %v", expectedPrefix, err)
	}
}
