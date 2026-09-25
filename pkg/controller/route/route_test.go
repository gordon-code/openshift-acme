package route

import (
	"bytes"
	"context"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"math/big"
	"math/rand"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"

	routeclientset "github.com/openshift/client-go/route/clientset/versioned/fake"
	"github.com/tnozicka/openshift-acme/pkg/api"
	kubeinformers "github.com/tnozicka/openshift-acme/pkg/machinery/informers/kube"
	routeinformers "github.com/tnozicka/openshift-acme/pkg/machinery/informers/route"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	apierrors "k8s.io/apimachinery/pkg/util/errors"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	coretesting "k8s.io/client-go/testing"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"
	"k8s.io/klog/v2"

	routev1 "github.com/openshift/api/route/v1"

	"github.com/tnozicka/openshift-acme/pkg/cert"
)

func init() {
	// Enable klog which is used in dependencies
	klog.InitFlags(nil)
	_ = flag.Set("logtostderr", "true")
	_ = flag.Set("v", "9")
}

func TestGetTemporaryName(t *testing.T) {
	tt := []struct {
		name string
		key  string
	}{
		{
			name: "empty key",
			key:  "",
		},
		{
			name: "simple key",
			key:  "my_route",
		},
		{
			name: "combined key",
			key:  "my_route:a.com/b/c/42",
		},
		{
			name: "long key",
			key:  utilrand.String(utilvalidation.DNS1035LabelMaxLength * 2),
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			r := getTemporaryName(tc.key)

			errs := validation.NameIsDNSSubdomain(r, false)
			if len(errs) != 0 {
				t.Errorf("name %q isn't DNS subdomain: %v", r, errs)
			}
		})
	}
}

func TestAdjustContainerResourceRequirements(t *testing.T) {
	tt := []struct {
		name                         string
		resourceRequirements         *corev1.ResourceRequirements
		limitRanges                  []*corev1.LimitRange
		expectedResourceRequirements *corev1.ResourceRequirements
		expectedErr                  error
	}{
		{
			name: "doesn't change with no LimitRange",
			resourceRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("50Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("100Mi"),
				},
			},
			limitRanges: nil,
			expectedResourceRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("50Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("100Mi"),
				},
			},
			expectedErr: nil,
		},
		{
			name: "doesn't change with unrelated LimitRange",
			resourceRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("50Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("150Mi"),
				},
			},
			limitRanges: []*corev1.LimitRange{
				{
					Spec: corev1.LimitRangeSpec{
						Limits: []corev1.LimitRangeItem{
							{
								Type: corev1.LimitTypePod,
								Min: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("50m"),
									corev1.ResourceMemory: resource.MustParse("25Mi"),
								},
								Max: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("300m"),
									corev1.ResourceMemory: resource.MustParse("200Mi"),
								},
								MaxLimitRequestRatio: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("2"),
									corev1.ResourceMemory: resource.MustParse("3"),
								},
							},
						},
					},
				},
			},
			expectedResourceRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("50Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("150Mi"),
				},
			},
			expectedErr: nil,
		},
		{
			name: "adjusts min to the LimitRange",
			resourceRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("50Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("100Mi"),
				},
			},
			limitRanges: []*corev1.LimitRange{
				{
					Spec: corev1.LimitRangeSpec{
						Limits: []corev1.LimitRangeItem{
							{
								Type: corev1.LimitTypePod,
								Min: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("300m"),
									corev1.ResourceMemory: resource.MustParse("200Mi"),
								},
								Max: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("500m"),
									corev1.ResourceMemory: resource.MustParse("500Mi"),
								},
							},
						},
					},
				},
			},
			expectedResourceRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("300m"),
					corev1.ResourceMemory: resource.MustParse("200Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("300m"),
					corev1.ResourceMemory: resource.MustParse("200Mi"),
				},
			},
			expectedErr: nil,
		},
		{
			name: "fails on higher resources then LimitRange max",
			resourceRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("50Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("100Mi"),
				},
			},
			limitRanges: []*corev1.LimitRange{
				{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "name",
						Namespace: "namespace",
					},
					Spec: corev1.LimitRangeSpec{
						Limits: []corev1.LimitRangeItem{
							{
								Type: corev1.LimitTypeContainer,
								Min: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("10m"),
									corev1.ResourceMemory: resource.MustParse("10Mi"),
								},
								Max: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("20m"),
									corev1.ResourceMemory: resource.MustParse("20Mi"),
								},
							},
						},
					},
				},
			},
			expectedResourceRequirements: nil,
			expectedErr: apierrors.NewAggregate([]error{
				fmt.Errorf("memory ask for 50Mi is higher then maximum memory from limitrange namespace/name"),
				fmt.Errorf("memory ask for 100Mi is higher then maximum memory from limitrange namespace/name"),
				fmt.Errorf("cpu ask for 100m is higher then maximum cpu from limitrange namespace/name"),
				fmt.Errorf("cpu ask for 200m is higher then maximum cpu from limitrange namespace/name"),
			}),
		},
		{
			name: "adjusts request to the LimitRange request ratio",
			resourceRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("100m"),
					corev1.ResourceMemory: resource.MustParse("100Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1000m"),
					corev1.ResourceMemory: resource.MustParse("1000Mi"),
				},
			},
			limitRanges: []*corev1.LimitRange{
				{
					Spec: corev1.LimitRangeSpec{
						Limits: []corev1.LimitRangeItem{
							{
								Type: corev1.LimitTypePod,
								MaxLimitRequestRatio: corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("5"),
									corev1.ResourceMemory: resource.MustParse("4"),
								},
							},
						},
					},
				},
			},
			expectedResourceRequirements: &corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("200m"),
					corev1.ResourceMemory: resource.MustParse("250Mi"),
				},
				Limits: corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("1000m"),
					corev1.ResourceMemory: resource.MustParse("1000Mi"),
				},
			},
			expectedErr: nil,
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			err := adjustContainerResourceRequirements(tc.resourceRequirements, tc.limitRanges)

			if !reflect.DeepEqual(err, tc.expectedErr) {
				t.Errorf("expected error %v, got %v", tc.expectedErr, err)
			}
			if err != nil {
				return
			}

			if !apiequality.Semantic.DeepEqual(tc.resourceRequirements, tc.expectedResourceRequirements) {
				t.Errorf("actual ResourceRequirements expected ones, diff: %s", cmp.Diff(tc.expectedResourceRequirements, tc.resourceRequirements))
			}
		})
	}
}

func TestFilterOutAnnotations(t *testing.T) {
	tt := []struct {
		name                string
		annotations         map[string]string
		expectedAnnotations map[string]string
	}{
		{
			name:                "nil annotations",
			annotations:         nil,
			expectedAnnotations: nil,
		},
		{
			name: "filters correctly",
			annotations: map[string]string{
				"http.exposer.acme.openshift.io/filter-out-annotations": "^matc[h]ing$",
				"foo": "bar",
				"haproxy.router.openshift.io/ip_whitelist": "10.0.0.0/16",
				"matching": "42",
			},
			expectedAnnotations: map[string]string{
				"http.exposer.acme.openshift.io/filter-out-annotations": "^matc[h]ing$",
				"foo": "bar",
			},
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			filterOutAnnotations(tc.annotations)

			if !apiequality.Semantic.DeepEqual(tc.annotations, tc.expectedAnnotations) {
				t.Errorf("expected annotations differ: %s", cmp.Diff(tc.expectedAnnotations, tc.annotations))
			}
		})
	}
}

func TestFilterOutLabels(t *testing.T) {
	tt := []struct {
		name           string
		labels         map[string]string
		annotations    map[string]string
		expectedLabels map[string]string
	}{
		{
			name:           "nil annotations",
			labels:         nil,
			annotations:    nil,
			expectedLabels: nil,
		},
		{
			name: "filters correctly",
			annotations: map[string]string{
				"http.exposer.acme.openshift.io/filter-out-labels": "^matc[h]ing$",
			},
			labels: map[string]string{
				"foo":      "bar",
				"matching": "42",
			},
			expectedLabels: map[string]string{
				"foo": "bar",
			},
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			var annotationsCopy map[string]string
			if tc.annotations != nil {
				annotationsCopy = map[string]string{}
				for k, v := range tc.annotations {
					annotationsCopy[k] = v
				}
			}

			filterOutLabels(tc.labels, tc.annotations)

			if !reflect.DeepEqual(tc.annotations, annotationsCopy) {
				t.Errorf("annotations were changed: %s", cmp.Diff(annotationsCopy, tc.annotations))
			}

			if !apiequality.Semantic.DeepEqual(tc.labels, tc.expectedLabels) {
				t.Errorf("expected labels differ: %s", cmp.Diff(tc.expectedLabels, tc.labels))
			}
		})
	}
}

func generateTestCertificate(t *testing.T, hosts []string, notBefore, notAfter time.Time) *cert.CertPemData {
	t.Helper()

	key, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate key: %v", err)
	}

	serialNumberLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serialNumber, err := cryptorand.Int(cryptorand.Reader, serialNumberLimit)
	if err != nil {
		t.Fatalf("failed to generate serial number: %v", err)
	}

	template := x509.Certificate{
		SerialNumber: serialNumber,
		Subject: pkix.Name{
			Organization: []string{"openshift-acme"},
		},
		NotBefore: notBefore,
		NotAfter:  notAfter,

		DNSNames: hosts,

		KeyUsage:              x509.KeyUsageKeyEncipherment | x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
	}

	derBytes, err := x509.CreateCertificate(cryptorand.Reader, &template, &template, key.Public(), key)
	if err != nil {
		t.Fatalf("failed to create certificate: %v", err)
	}

	certBuffer := &bytes.Buffer{}
	if err := pem.Encode(certBuffer, &pem.Block{Type: "CERTIFICATE", Bytes: derBytes}); err != nil {
		t.Fatalf("failed to encode certificate: %v", err)
	}

	return &cert.CertPemData{
		Crt: certBuffer.Bytes(),
		Key: pem.EncodeToMemory(&pem.Block{
			Type:  "RSA PRIVATE KEY",
			Bytes: x509.MarshalPKCS1PrivateKey(key),
		}),
	}
}

func TestNeedsCertKey(t *testing.T) {
	// Use a fixed, zero-nanosecond timestamp: x509 certs round-trip NotBefore/
	// NotAfter at 1-second precision, so sub-second components here would
	// desync the in-memory "now" from the parsed certificate's timestamps.
	now := time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC)
	const host = "example.com"

	healthyCert := generateTestCertificate(t, []string{host}, now.Add(-20*24*time.Hour), now.Add(80*24*time.Hour))
	renewalCert := generateTestCertificate(t, []string{host}, now.Add(-70*24*time.Hour), now.Add(30*24*time.Hour))
	proactiveCert := generateTestCertificate(t, []string{host}, now.Add(-60*24*time.Hour), now.Add(40*24*time.Hour))
	expiredCert := generateTestCertificate(t, []string{host}, now.Add(-1000*24*time.Hour), now.Add(-1*24*time.Hour))
	mismatchedHostCert := generateTestCertificate(t, []string{"other.example.com"}, now.Add(-1*time.Hour), now.Add(1000*time.Hour))

	// The proactive-renewal window (lifetime/3 < remains <= lifetime/2) is a
	// coin flip seeded by rand.NewSource(t.UnixNano()). Rather than hardcode a
	// magic timestamp that happens to land on one side, compute the expected
	// outcome using the exact same seed/formula used by needsCertKey. This
	// still catches regressions in needsCertKey's own comparison/arithmetic,
	// since needsCertKey computes n independently at call time.
	s := rand.NewSource(now.UnixNano())
	r := rand.New(s)
	n := r.NormFloat64()*RenewalStandardDeviation + RenewalMean
	expectedProactiveReason := ""
	if n < 0 {
		expectedProactiveReason = "Proactive renewal"
	}

	tt := []struct {
		name           string
		route          *routev1.Route
		expectedReason string
		expectedErr    error
	}{
		{
			name:           "missing CertKey - nil TLS",
			route:          &routev1.Route{Spec: routev1.RouteSpec{Host: host}},
			expectedReason: "Route is missing CertKey",
		},
		{
			name: "missing CertKey - empty cert and key",
			route: &routev1.Route{
				Spec: routev1.RouteSpec{Host: host, TLS: &routev1.TLSConfig{}},
			},
			expectedReason: "Route is missing CertKey",
		},
		{
			name: "missing CertKey - key present but certificate empty",
			route: &routev1.Route{
				Spec: routev1.RouteSpec{Host: host, TLS: &routev1.TLSConfig{Key: "somekey"}},
			},
			expectedReason: "Route is missing CertKey",
		},
		{
			name: "hostname mismatch",
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Host: host,
					TLS: &routev1.TLSConfig{
						Certificate: string(mismatchedHostCert.Crt),
						Key:         string(mismatchedHostCert.Key),
					},
				},
			},
			expectedReason: "Existing certificate doesn't match hostname",
		},
		{
			name: "already expired",
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Host: host,
					TLS: &routev1.TLSConfig{
						Certificate: string(expiredCert.Crt),
						Key:         string(expiredCert.Key),
					},
				},
			},
			expectedReason: "Already expired",
		},
		{
			name: "in renewal period",
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Host: host,
					TLS: &routev1.TLSConfig{
						Certificate: string(renewalCert.Crt),
						Key:         string(renewalCert.Key),
					},
				},
			},
			expectedReason: "In renewal period",
		},
		{
			name: "proactive renewal window",
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Host: host,
					TLS: &routev1.TLSConfig{
						Certificate: string(proactiveCert.Crt),
						Key:         string(proactiveCert.Key),
					},
				},
			},
			expectedReason: expectedProactiveReason,
		},
		{
			name: "healthy cert - no renewal needed",
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Host: host,
					TLS: &routev1.TLSConfig{
						Certificate: string(healthyCert.Crt),
						Key:         string(healthyCert.Key),
					},
				},
			},
			expectedReason: "",
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			reason, err := needsCertKey(now, tc.route)

			if !reflect.DeepEqual(err, tc.expectedErr) {
				t.Errorf("expected error %v, got %v", tc.expectedErr, err)
			}
			if reason != tc.expectedReason {
				t.Errorf("expected reason %q, got %q", tc.expectedReason, reason)
			}
		})
	}
}

func TestExposerPodSecurityContext(t *testing.T) {
	trueVal := true
	expected := &corev1.PodSecurityContext{
		RunAsNonRoot: &trueVal,
		SeccompProfile: &corev1.SeccompProfile{
			Type: corev1.SeccompProfileTypeRuntimeDefault,
		},
	}

	got := exposerPodSecurityContext()

	if !apiequality.Semantic.DeepEqual(got, expected) {
		t.Errorf("unexpected PodSecurityContext, diff: %s", cmp.Diff(expected, got))
	}
}

func TestExposerContainerSecurityContext(t *testing.T) {
	trueVal := true
	falseVal := false
	expected := &corev1.SecurityContext{
		AllowPrivilegeEscalation: &falseVal,
		ReadOnlyRootFilesystem:   &trueVal,
		Capabilities: &corev1.Capabilities{
			Drop: []corev1.Capability{"ALL"},
		},
	}

	got := exposerContainerSecurityContext()

	if !apiequality.Semantic.DeepEqual(got, expected) {
		t.Errorf("unexpected SecurityContext, diff: %s", cmp.Diff(expected, got))
	}
}

func newTestRouteController() (*RouteController, *routeclientset.Clientset, kubeinformers.Interface, routeinformers.Interface) {
	kubeClient := fake.NewSimpleClientset()
	routeClient := routeclientset.NewSimpleClientset()

	kubeInf := kubeinformers.NewKubeInformersForNamespaces(kubeClient, []string{""})
	routeInf := routeinformers.NewRouteInformersForNamespaces(routeClient, []string{""})

	rc := NewRouteController(
		"kubernetes.io/tls-acme",
		1*time.Second,
		1*time.Minute,
		2048,
		"exposer:latest",
		"default",
		kubeClient,
		kubeInf,
		routeClient,
		routeInf,
	)

	return rc, routeClient, kubeInf, routeInf
}

func TestSync_ContextTimeout(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer ts.Close()

	rc, _, kubeInf, routeInf := newTestRouteController()

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-issuer",
			Namespace: "default",
		},
		Data: map[string]string{
			"cert-issuer.types.acme.openshift.io": `type: ACME
secretName: letsencrypt-live
acmeCertIssuer:
  directoryURL: ` + ts.URL,
		},
	}
	_ = kubeInf.InformersForOrGlobal("default").Core().V1().ConfigMaps().Informer().GetIndexer().Add(cm)

	key, _ := rsa.GenerateKey(cryptorand.Reader, 2048)
	keyPem := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(key)})

	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "letsencrypt-live",
			Namespace: "default",
		},
		Data: map[string][]byte{
			corev1.TLSCertKey:       []byte(ts.URL),
			corev1.TLSPrivateKeyKey: keyPem,
		},
	}
	_ = kubeInf.InformersForOrGlobal("default").Core().V1().Secrets().Informer().GetIndexer().Add(secret)

	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "default",
			Annotations: map[string]string{
				"kubernetes.io/tls-acme":             "true",
				"acme.openshift.io/cert-issuer-name": "test-issuer",
			},
		},
		Spec: routev1.RouteSpec{
			Host: "example.com",
		},
		Status: routev1.RouteStatus{
			Ingress: []routev1.RouteIngress{
				{
					Conditions: []routev1.RouteIngressCondition{
						{
							Type:   routev1.RouteAdmitted,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
		},
	}
	_ = routeInf.InformersForOrGlobal("default").Route().V1().Routes().Informer().GetIndexer().Add(route)

	rc.acmeTimeout = 100 * time.Millisecond

	err := rc.sync(context.Background(), "default/test-route")

	if err == nil {
		t.Fatalf("Expected an error from sync due to ACME timeout, got nil")
	}
	if !strings.Contains(err.Error(), "context deadline exceeded") {
		t.Errorf("Expected context deadline exceeded error, got: %v", err)
	}
}
func TestUpdateStatus_MergePatch(t *testing.T) {
	rc, routeClient, _, _ := newTestRouteController()
	ctx := context.TODO()

	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "default",
			// Annotations deliberately left nil to test the nil-map nil-pointer issue!
		},
	}
	routeClient.RouteV1().Routes("default").Create(ctx, route, metav1.CreateOptions{})

	status := &api.Status{
		ObservedGeneration: 1,
		ProvisioningStatus: api.CertProvisioningStatus{
			StartedAt:   time.Now(),
			OrderURI:    "http://example.com/order",
			OrderStatus: "pending",
		},
	}

	patched := false
	routeClient.PrependReactor("patch", "routes", func(action coretesting.Action) (handled bool, ret runtime.Object, err error) {
		patchAction := action.(coretesting.PatchAction)

		if patchAction.GetPatchType() != types.MergePatchType {
			t.Errorf("Expected MergePatchType, got %v", patchAction.GetPatchType())
		}

		patchData := patchAction.GetPatch()
		var payload map[string]interface{}
		if err := json.Unmarshal(patchData, &payload); err != nil {
			t.Fatalf("Failed to parse patch data: %v", err)
		}

		metadata, ok := payload["metadata"].(map[string]interface{})
		if !ok {
			t.Fatalf("Patch missing metadata block: %v", payload)
		}

		annotations, ok := metadata["annotations"].(map[string]interface{})
		if !ok {
			t.Fatalf("Patch missing annotations block: %v", metadata)
		}

		if _, exists := annotations[api.AcmeStatusAnnotation]; !exists {
			t.Errorf("Patch missing %q annotation, got: %v", api.AcmeStatusAnnotation, annotations)
		}

		patched = true
		return true, route, nil
	})

	err := rc.updateStatus(ctx, route, status)
	if err != nil {
		t.Fatalf("updateStatus failed: %v", err)
	}

	if !patched {
		t.Error("Expected Patch reactor to be invoked")
	}
}
