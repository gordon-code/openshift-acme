package acme

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"

	api "github.com/tnozicka/openshift-acme/pkg/api"
	"github.com/tnozicka/openshift-acme/pkg/machinery/informers/kube"
	"gopkg.in/yaml.v2"
)

func TestAccountController_Sync_ContextTimeout(t *testing.T) {
	// Start a slow ACME server mock
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(50 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	client := fake.NewSimpleClientset()
	informers := kube.NewKubeInformersForNamespaces(client, []string{"default"})

	ac := &AccountController{
		kubeClient:                 client,
		kubeInformersForNamespaces: informers,
		acmeTimeout:                1 * time.Millisecond,
		recorder:                   &record.FakeRecorder{},
		queue:                      workqueue.NewTypedRateLimitingQueue(workqueue.DefaultTypedControllerRateLimiter[string]()),
	}

	// Create Issuer ConfigMap pointing to the slow server
	issuerData := api.CertIssuer{
		Type: api.CertIssuerTypeAcme,
		AcmeCertIssuer: &api.AcmeCertIssuer{
			DirectoryURL: ts.URL,
		},
	}
	issuerDataBytes, _ := yaml.Marshal(issuerData)

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-issuer",
			Namespace: "default",
		},
		Data: map[string]string{
			api.CertIssuerDataKey: string(issuerDataBytes),
		},
	}

	// Inject into informer cache
	informer := informers.InformersForOrGlobal("default").Core().V1().ConfigMaps().Informer()
	_ = informer.GetIndexer().Add(cm)

	err := ac.sync(context.Background(), "default/test-issuer")
	if err == nil {
		t.Fatalf("expected error from sync due to ACME timeout, got nil")
	}

	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("expected 'context deadline exceeded' error, got: %v", err)
	}
}
