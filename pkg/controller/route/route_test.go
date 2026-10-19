package route

import (
	"bytes"
	"context"
	"crypto"
	cryptorand "crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/acme"

	"github.com/gordon-code/openshift-acme/pkg/api"
	kubeinformers "github.com/gordon-code/openshift-acme/pkg/machinery/informers/kube"
	routeinformers "github.com/gordon-code/openshift-acme/pkg/machinery/informers/route"
	routeclientset "github.com/openshift/client-go/route/clientset/versioned/fake"
	corev1 "k8s.io/api/core/v1"
	apiequality "k8s.io/apimachinery/pkg/api/equality"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	apierrors "k8s.io/apimachinery/pkg/util/errors"
	utilrand "k8s.io/apimachinery/pkg/util/rand"
	utilvalidation "k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/kubernetes/fake"
	coretesting "k8s.io/client-go/testing"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	routev1 "github.com/openshift/api/route/v1"

	"github.com/gordon-code/openshift-acme/pkg/cert"
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
				t.Errorf("actual ResourceRequirements differed from expected: expected %+v, got %+v", tc.expectedResourceRequirements, tc.resourceRequirements)
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
				t.Errorf("expected annotations differ: expected %+v, got %+v", tc.expectedAnnotations, tc.annotations)
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
				t.Errorf("annotations were changed: expected %+v, got %+v", annotationsCopy, tc.annotations)
			}

			if !apiequality.Semantic.DeepEqual(tc.labels, tc.expectedLabels) {
				t.Errorf("expected labels differ: expected %+v, got %+v", tc.expectedLabels, tc.labels)
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
	expiredCert := generateTestCertificate(t, []string{host}, now.Add(-1000*24*time.Hour), now.Add(-1*24*time.Hour))
	mismatchedHostCert := generateTestCertificate(t, []string{"other.example.com"}, now.Add(-1*time.Hour), now.Add(1000*time.Hour))

	// Generate a certificate and evaluate it both just inside and just outside its
	// deterministic proactive renewal threshold in [lifetime/3, lifetime/2).
	proactiveBaseCert := generateTestCertificate(t, []string{host}, now.Add(-60*24*time.Hour), now.Add(40*24*time.Hour))
	parsedProactiveCert, err := proactiveBaseCert.Certificate()
	if err != nil {
		t.Fatalf("failed to parse proactive test cert: %v", err)
	}
	proactiveThreshold := proactiveRenewalThreshold(parsedProactiveCert, 100*24*time.Hour)
	if proactiveThreshold < (100*24*time.Hour)/3 || proactiveThreshold >= (100*24*time.Hour)/2 {
		t.Fatalf("expected proactiveThreshold in [lifetime/3, lifetime/2), got %v", proactiveThreshold)
	}

	// 64-day, 47-day, and 160-hour (shortlived) Let's Encrypt certificate lifecycles
	healthy64DayCert := generateTestCertificate(t, []string{host}, now.Add(-14*24*time.Hour), now.Add(50*24*time.Hour))
	parsedHealthy64, _ := healthy64DayCert.Certificate()
	threshold64 := proactiveRenewalThreshold(parsedHealthy64, 64*24*time.Hour)

	renewal64DayCert := generateTestCertificate(t, []string{host}, now.Add(-44*24*time.Hour), now.Add(20*24*time.Hour))

	healthy47DayCert := generateTestCertificate(t, []string{host}, now.Add(-7*24*time.Hour), now.Add(40*24*time.Hour))
	parsedHealthy47, _ := healthy47DayCert.Certificate()
	threshold47 := proactiveRenewalThreshold(parsedHealthy47, 47*24*time.Hour)

	renewal47DayCert := generateTestCertificate(t, []string{host}, now.Add(-32*24*time.Hour), now.Add(15*24*time.Hour))

	healthyShortLivedCert := generateTestCertificate(t, []string{host}, now.Add(-40*time.Hour), now.Add(120*time.Hour))
	parsedHealthyShortLived, _ := healthyShortLivedCert.Certificate()
	thresholdShortLived := proactiveRenewalThreshold(parsedHealthyShortLived, 160*time.Hour)

	renewalShortLivedCert := generateTestCertificate(t, []string{host}, now.Add(-110*time.Hour), now.Add(50*time.Hour))

	parsedHealthy100, _ := healthyCert.Certificate()
	threshold100 := proactiveRenewalThreshold(parsedHealthy100, 100*24*time.Hour)

	tt := []struct {
		name            string
		evalTime        time.Time
		route           *routev1.Route
		expectedReason  string
		expectedRequeue time.Duration
		expectedErr     error
	}{
		{
			name:           "missing CertKey - nil TLS",
			evalTime:       now,
			route:          &routev1.Route{Spec: routev1.RouteSpec{Host: host}},
			expectedReason: "Route is missing CertKey",
		},
		{
			name:     "missing CertKey - empty cert and key",
			evalTime: now,
			route: &routev1.Route{
				Spec: routev1.RouteSpec{Host: host, TLS: &routev1.TLSConfig{}},
			},
			expectedReason: "Route is missing CertKey",
		},
		{
			name:     "missing CertKey - key present but certificate empty",
			evalTime: now,
			route: &routev1.Route{
				Spec: routev1.RouteSpec{Host: host, TLS: &routev1.TLSConfig{Key: "somekey"}},
			},
			expectedReason: "Route is missing CertKey",
		},
		{
			name:     "hostname mismatch",
			evalTime: now,
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
			name:     "already expired",
			evalTime: now,
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
			name:     "in renewal period",
			evalTime: now,
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
			name:     "proactive renewal window - at deterministic threshold triggers renewal",
			evalTime: parsedProactiveCert.NotAfter.Add(-proactiveThreshold),
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Host: host,
					TLS: &routev1.TLSConfig{
						Certificate: string(proactiveBaseCert.Crt),
						Key:         string(proactiveBaseCert.Key),
					},
				},
			},
			expectedReason:  "Proactive renewal",
			expectedRequeue: 0,
		},
		{
			name:     "proactive renewal window - 1 hour before deterministic threshold schedules exact requeue",
			evalTime: parsedProactiveCert.NotAfter.Add(-proactiveThreshold - time.Hour),
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Host: host,
					TLS: &routev1.TLSConfig{
						Certificate: string(proactiveBaseCert.Crt),
						Key:         string(proactiveBaseCert.Key),
					},
				},
			},
			expectedReason:  "",
			expectedRequeue: time.Hour,
		},
		{
			name:     "healthy cert - no renewal needed",
			evalTime: now,
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Host: host,
					TLS: &routev1.TLSConfig{
						Certificate: string(healthyCert.Crt),
						Key:         string(healthyCert.Key),
					},
				},
			},
			expectedReason:  "",
			expectedRequeue: 80*24*time.Hour - threshold100,
		},
		{
			name:     "64-day cert - healthy schedules requeue at deterministic threshold",
			evalTime: now,
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Host: host,
					TLS: &routev1.TLSConfig{
						Certificate: string(healthy64DayCert.Crt),
						Key:         string(healthy64DayCert.Key),
					},
				},
			},
			expectedReason:  "",
			expectedRequeue: 50*24*time.Hour - threshold64,
		},
		{
			name:     "64-day cert - in renewal period",
			evalTime: now,
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Host: host,
					TLS: &routev1.TLSConfig{
						Certificate: string(renewal64DayCert.Crt),
						Key:         string(renewal64DayCert.Key),
					},
				},
			},
			expectedReason: "In renewal period",
		},
		{
			name:     "47-day cert - healthy schedules requeue at deterministic threshold",
			evalTime: now,
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Host: host,
					TLS: &routev1.TLSConfig{
						Certificate: string(healthy47DayCert.Crt),
						Key:         string(healthy47DayCert.Key),
					},
				},
			},
			expectedReason:  "",
			expectedRequeue: 40*24*time.Hour - threshold47,
		},
		{
			name:     "47-day cert - in renewal period",
			evalTime: now,
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Host: host,
					TLS: &routev1.TLSConfig{
						Certificate: string(renewal47DayCert.Crt),
						Key:         string(renewal47DayCert.Key),
					},
				},
			},
			expectedReason: "In renewal period",
		},
		{
			name:     "shortlived 160h cert - healthy schedules requeue at deterministic threshold",
			evalTime: now,
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Host: host,
					TLS: &routev1.TLSConfig{
						Certificate: string(healthyShortLivedCert.Crt),
						Key:         string(healthyShortLivedCert.Key),
					},
				},
			},
			expectedReason:  "",
			expectedRequeue: 120*time.Hour - thresholdShortLived,
		},
		{
			name:     "shortlived 160h cert - in renewal period",
			evalTime: now,
			route: &routev1.Route{
				Spec: routev1.RouteSpec{
					Host: host,
					TLS: &routev1.TLSConfig{
						Certificate: string(renewalShortLivedCert.Crt),
						Key:         string(renewalShortLivedCert.Key),
					},
				},
			},
			expectedReason: "In renewal period",
		},
	}

	for _, tc := range tt {
		t.Run(tc.name, func(t *testing.T) {
			reason, requeueAfter, err := needsCertKey(tc.evalTime, tc.route)

			if !reflect.DeepEqual(err, tc.expectedErr) {
				t.Errorf("expected error %v, got %v", tc.expectedErr, err)
			}
			if reason != tc.expectedReason {
				t.Errorf("expected reason %q, got %q", tc.expectedReason, reason)
			}
			if requeueAfter != tc.expectedRequeue {
				t.Errorf("expected requeueAfter %v, got %v", tc.expectedRequeue, requeueAfter)
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
		t.Errorf("unexpected PodSecurityContext: expected %+v, got %+v", expected, got)
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
		t.Errorf("unexpected SecurityContext: expected %+v, got %+v", expected, got)
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
		"",
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
	_, _ = routeClient.RouteV1().Routes("default").Create(ctx, route, metav1.CreateOptions{})

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

type recordingRateLimitingQueue struct {
	workqueue.TypedRateLimitingInterface[string]
	addedAfter map[string]time.Duration
}

func (q *recordingRateLimitingQueue) AddAfter(item string, duration time.Duration) {
	q.addedAfter[item] = duration
	q.TypedRateLimitingInterface.AddAfter(item, duration)
}

func TestSync_SchedulesRenewalRequeueForHealthyCert(t *testing.T) {
	rc, _, _, routeInf := newTestRouteController()
	recorderQueue := &recordingRateLimitingQueue{
		TypedRateLimitingInterface: rc.queue,
		addedAfter:                 make(map[string]time.Duration),
	}
	rc.queue = recorderQueue

	now := time.Now()
	// 47-day Let's Encrypt certificate issued 7 days ago (40 days remaining;
	// proactive renewal window is [lifetime/3, lifetime/2)).
	certData := generateTestCertificate(t, []string{"example.com"}, now.Add(-7*24*time.Hour), now.Add(40*24*time.Hour))
	parsedCert, err := certData.Certificate()
	if err != nil {
		t.Fatalf("failed to parse test cert: %v", err)
	}
	threshold := proactiveRenewalThreshold(parsedCert, 47*24*time.Hour)

	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "default",
			Annotations: map[string]string{
				"kubernetes.io/tls-acme": "true",
			},
		},
		Spec: routev1.RouteSpec{
			Host: "example.com",
			TLS: &routev1.TLSConfig{
				Certificate: string(certData.Crt),
				Key:         string(certData.Key),
			},
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

	err = rc.sync(context.Background(), "default/test-route")
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	delay, ok := recorderQueue.addedAfter["default/test-route"]
	if !ok {
		t.Fatal("expected Route to be scheduled via queue.AddAfter for future renewal check, but it was not")
	}

	expectedDelay := 40*24*time.Hour - threshold
	// Allow a few seconds tolerance for wall-clock advancement between cert generation and sync().
	if diff := delay - expectedDelay; diff < -10*time.Second || diff > 10*time.Second {
		t.Errorf("expected requeue delay ~%v, got %v (diff %v)", expectedDelay, delay, diff)
	}
}

func TestSync_DoesNotScheduleRenewalRequeueWhenContinuingPendingOrder(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer ts.Close()

	rc, routeClient, kubeInf, routeInf := newTestRouteController()
	recorderQueue := &recordingRateLimitingQueue{
		TypedRateLimitingInterface: rc.queue,
		addedAfter:                 make(map[string]time.Duration),
	}
	rc.queue = recorderQueue

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

	now := time.Now()
	// Healthy certificate (lifetime=64d, remains=50d) with an active pending order in flight
	certData := generateTestCertificate(t, []string{"example.com"}, now.Add(-14*24*time.Hour), now.Add(50*24*time.Hour))

	route := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-route",
			Namespace: "default",
			Annotations: map[string]string{
				"kubernetes.io/tls-acme":             "true",
				"acme.openshift.io/cert-issuer-name": "test-issuer",
				api.AcmeStatusAnnotation:             `{"provisioningStatus":{"orderURI":"` + ts.URL + `/order/1","orderStatus":"pending"}}`,
			},
		},
		Spec: routev1.RouteSpec{
			Host: "example.com",
			TLS: &routev1.TLSConfig{
				Certificate: string(certData.Crt),
				Key:         string(certData.Key),
			},
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
	_, _ = routeClient.RouteV1().Routes("default").Create(context.Background(), route, metav1.CreateOptions{})
	_ = routeInf.InformersForOrGlobal("default").Route().V1().Routes().Informer().GetIndexer().Add(route)

	err := rc.sync(context.Background(), "default/test-route")
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}

	if delay, ok := recorderQueue.addedAfter["default/test-route"]; ok {
		t.Errorf("expected no long-horizon renewal requeue while continuing pending order, got %v", delay)
	}
}

func TestProactiveRenewalThreshold(t *testing.T) {
	now := time.Now()
	certA := generateTestCertificate(t, []string{"a.example.com"}, now, now.Add(47*24*time.Hour))
	parsedA, err := certA.Certificate()
	if err != nil {
		t.Fatalf("failed to parse certA: %v", err)
	}

	if got := proactiveRenewalThreshold(parsedA, 0); got != 0 {
		t.Errorf("expected 0 threshold for zero lifetime, got %v", got)
	}
	if got := proactiveRenewalThreshold(parsedA, -6*time.Hour); got != -2*time.Hour {
		t.Errorf("expected lifetime/3 (-2h) for negative lifetime, got %v", got)
	}

	lifetime := 47 * 24 * time.Hour
	first := proactiveRenewalThreshold(parsedA, lifetime)
	second := proactiveRenewalThreshold(parsedA, lifetime)
	if first != second {
		t.Errorf("expected deterministic threshold for identical cert, got %v != %v", first, second)
	}

	seen := map[time.Duration]bool{first: true}
	for i := 0; i < 5; i++ {
		c := generateTestCertificate(t, []string{fmt.Sprintf("host-%d.example.com", i)}, now, now.Add(lifetime))
		parsed, err := c.Certificate()
		if err != nil {
			t.Fatalf("failed to parse cert %d: %v", i, err)
		}
		th := proactiveRenewalThreshold(parsed, lifetime)
		if th < lifetime/3 || th >= lifetime/2 {
			t.Errorf("expected threshold in [lifetime/3, lifetime/2), got %v", th)
		}
		seen[th] = true
	}
	if len(seen) < 2 {
		t.Errorf("expected distinct certificates to produce distinct thresholds across the window, got %d unique value(s)", len(seen))
	}
}

func TestCertProfileForRoute(t *testing.T) {
	routeWithOverride := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				api.AcmeCertProfile: "  shortlived  ",
			},
		},
	}
	routeWithWhitespaceOnly := &routev1.Route{
		ObjectMeta: metav1.ObjectMeta{
			Annotations: map[string]string{
				api.AcmeCertProfile: "   ",
			},
		},
	}
	routeWithoutOverride := &routev1.Route{}

	if got := certProfileForRoute(routeWithOverride, "tlsserver"); got != "shortlived" {
		t.Errorf("expected trimmed route annotation override %q, got %q", "shortlived", got)
	}
	if got := certProfileForRoute(routeWithWhitespaceOnly, "  tlsserver  "); got != "tlsserver" {
		t.Errorf("expected whitespace-only annotation to fall back to trimmed default %q, got %q", "tlsserver", got)
	}
	if got := certProfileForRoute(routeWithoutOverride, "tlsserver"); got != "tlsserver" {
		t.Errorf("expected default profile fallback %q, got %q", "tlsserver", got)
	}
	if got := certProfileForRoute(nil, "  classic  "); got != "classic" {
		t.Errorf("expected nil route to return trimmed default profile %q, got %q", "classic", got)
	}
	if got := certProfileForRoute(routeWithoutOverride, "   "); got != "" {
		t.Errorf("expected empty profile when neither route nor default sets one, got %q", got)
	}
}

func TestRewriteNewOrderJWSWithProfile_EdgeCases(t *testing.T) {
	key, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	// Empty payload (e.g. POST-as-GET) should be returned unchanged
	postAsGet := []byte(`{"protected":"eyJhbGciOiJSUzI1NiJ9","payload":"","signature":"c2ln"}`)
	out, err := rewriteNewOrderJWSWithProfile(postAsGet, "tlsserver", key)
	if err != nil {
		t.Fatalf("unexpected error for empty payload JWS: %v", err)
	}
	if !bytes.Equal(out, postAsGet) {
		t.Errorf("expected empty-payload JWS to be returned unchanged, got %s", string(out))
	}

	// Malformed JSON should return an error
	if _, err := rewriteNewOrderJWSWithProfile([]byte(`not-json`), "tlsserver", key); err == nil {
		t.Errorf("expected error for malformed JWS JSON, got nil")
	}
}

func TestSync_AuthorizeOrderWithProfile(t *testing.T) {
	key, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}

	var (
		receivedProfile     string
		receivedIdentifiers []struct {
			Type  string `json:"type"`
			Value string `json:"value"`
		}
		signatureVerifyErr error
		includeProfiles    = true
	)
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Replay-Nonce", "test-nonce")
		switch r.URL.Path {
		case "/directory":
			w.Header().Set("Content-Type", "application/json")
			if !includeProfiles {
				_, _ = fmt.Fprintf(w, `{
					"newNonce": %q,
					"newAccount": %q,
					"newOrder": %q
				}`, ts.URL+"/new-nonce", ts.URL+"/new-account", ts.URL+"/new-order")
				return
			}
			_, _ = fmt.Fprintf(w, `{
				"newNonce": %q,
				"newAccount": %q,
				"newOrder": %q,
				"meta": {
					"profiles": {
						"classic": "Classic 64-day/45-day profile",
						"tlsserver": "Modern 45-day profile",
						"shortlived": "Short-lived 160-hour profile"
					}
				}
			}`, ts.URL+"/new-nonce", ts.URL+"/new-account", ts.URL+"/new-order")
		case "/new-nonce":
			w.WriteHeader(http.StatusNoContent)
		case "/new-account":
			w.Header().Set("Location", ts.URL+"/account/1")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"status":"valid"}`))
		case "/new-order":
			body, _ := io.ReadAll(r.Body)
			var jws struct {
				Protected string `json:"protected"`
				Payload   string `json:"payload"`
				Signature string `json:"signature"`
			}
			_ = json.Unmarshal(body, &jws)
			sigBytes, decErr := base64.RawURLEncoding.DecodeString(jws.Signature)
			if decErr != nil {
				signatureVerifyErr = decErr
			} else {
				digest := sha256.Sum256([]byte(jws.Protected + "." + jws.Payload))
				signatureVerifyErr = rsa.VerifyPKCS1v15(&key.PublicKey, crypto.SHA256, digest[:], sigBytes)
			}

			payloadBytes, _ := base64.RawURLEncoding.DecodeString(jws.Payload)
			var orderReq struct {
				Profile     string `json:"profile"`
				Identifiers []struct {
					Type  string `json:"type"`
					Value string `json:"value"`
				} `json:"identifiers"`
			}
			_ = json.Unmarshal(payloadBytes, &orderReq)
			receivedProfile = orderReq.Profile
			receivedIdentifiers = orderReq.Identifiers

			w.Header().Set("Location", ts.URL+"/order/1")
			w.WriteHeader(http.StatusCreated)
			_, _ = fmt.Fprintf(w, `{"status":"pending","authorizations":[%q]}`, ts.URL+"/authz/1")
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	rc, routeClient, kubeInf, routeInf := newTestRouteController()
	rc.certDefaultProfile = "tlsserver"

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-issuer",
			Namespace: "default",
		},
		Data: map[string]string{
			"cert-issuer.types.acme.openshift.io": `type: ACME
secretName: letsencrypt-live
acmeCertIssuer:
  directoryURL: ` + ts.URL + `/directory`,
		},
	}
	_ = kubeInf.InformersForOrGlobal("default").Core().V1().ConfigMaps().Informer().GetIndexer().Add(cm)

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
				api.AcmeCertProfile:                  "shortlived",
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
	_, _ = routeClient.RouteV1().Routes("default").Create(context.Background(), route, metav1.CreateOptions{})
	_ = routeInf.InformersForOrGlobal("default").Route().V1().Routes().Informer().GetIndexer().Add(route)

	if err := rc.sync(context.Background(), "default/test-route"); err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if signatureVerifyErr != nil {
		t.Errorf("expected valid RSA SHA-256 signature on rewritten newOrder JWS, got error: %v", signatureVerifyErr)
	}
	if receivedProfile != "shortlived" {
		t.Errorf("expected new-order payload to include route override profile %q, got %q", "shortlived", receivedProfile)
	}
	if len(receivedIdentifiers) != 1 || receivedIdentifiers[0].Value != "example.com" {
		t.Errorf("expected new-order payload to preserve domain identifier example.com, got %+v", receivedIdentifiers)
	}

	// Verify controller default profile is used when Route annotation is omitted
	routeDefaultProfile := route.DeepCopy()
	delete(routeDefaultProfile.Annotations, api.AcmeCertProfile)
	delete(routeDefaultProfile.Annotations, api.AcmeStatusAnnotation)
	_ = routeInf.InformersForOrGlobal("default").Route().V1().Routes().Informer().GetIndexer().Update(routeDefaultProfile)

	if err := rc.sync(context.Background(), "default/test-route"); err != nil {
		t.Fatalf("sync with default profile failed: %v", err)
	}
	if signatureVerifyErr != nil {
		t.Errorf("expected valid RSA SHA-256 signature on rewritten newOrder JWS, got error: %v", signatureVerifyErr)
	}
	if receivedProfile != "tlsserver" {
		t.Errorf("expected new-order payload to include controller default profile %q, got %q", "tlsserver", receivedProfile)
	}

	// Verify unsupported profile is rejected before order creation
	routeUnsupported := route.DeepCopy()
	routeUnsupported.Annotations[api.AcmeCertProfile] = "nonexistent-profile"
	delete(routeUnsupported.Annotations, api.AcmeStatusAnnotation)
	_ = routeInf.InformersForOrGlobal("default").Route().V1().Routes().Informer().GetIndexer().Update(routeUnsupported)

	err = rc.sync(context.Background(), "default/test-route")
	if err == nil || !strings.Contains(err.Error(), "does not advertise profile") {
		t.Errorf("expected error for unsupported profile, got: %v", err)
	}

	// Verify CA directory without meta.profiles returns clear error
	includeProfiles = false
	routeNoProfiles := route.DeepCopy()
	routeNoProfiles.Annotations[api.AcmeCertProfile] = "tlsserver"
	delete(routeNoProfiles.Annotations, api.AcmeStatusAnnotation)
	_ = routeInf.InformersForOrGlobal("default").Route().V1().Routes().Informer().GetIndexer().Update(routeNoProfiles)

	err = rc.sync(context.Background(), "default/test-route")
	if err == nil || !strings.Contains(err.Error(), "does not advertise support for profiles") {
		t.Errorf("expected error when CA does not advertise support for profiles, got: %v", err)
	}
}

type bodyCapturingRoundTripper struct {
	base     http.RoundTripper
	lastBody []byte
}

func (rt *bodyCapturingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := rt.base.RoundTrip(req)
	if err != nil || resp == nil || resp.Body == nil {
		return resp, err
	}
	bodyBytes, readErr := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	resp.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	if readErr == nil {
		rt.lastBody = bodyBytes
	}
	return resp, nil
}

func TestLiveLetsEncryptStagingIntegration(t *testing.T) {
	if os.Getenv("RUN_LETSENCRYPT_STAGING_TESTS") == "" {
		t.Skip("skipping live Let's Encrypt Staging integration test; set RUN_LETSENCRYPT_STAGING_TESTS=1 to enable")
	}

	const stagingDirectoryURL = "https://acme-staging-v02.api.letsencrypt.org/directory"
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// 1. Register an ephemeral ACME account against live Let's Encrypt Staging
	accountKey, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
	capturingRT := &bodyCapturingRoundTripper{base: http.DefaultTransport}
	client := &acme.Client{
		DirectoryURL: stagingDirectoryURL,
		Key:          accountKey,
		HTTPClient:   &http.Client{Transport: capturingRT},
		UserAgent:    "github.com/gordon-code/openshift-acme-ci-test",
	}
	account, err := client.Register(ctx, &acme.Account{}, acme.AcceptTOS)
	if err != nil {
		t.Fatalf("failed to register ephemeral account with Let's Encrypt Staging: %v", err)
	}
	t.Logf("Registered ephemeral Let's Encrypt Staging account: %s (status=%s)", account.URI, account.Status)

	// Deactivate the ephemeral account when the test finishes so we leave Staging clean
	defer func() {
		deactivateCtx, deactivateCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer deactivateCancel()
		if err := client.DeactivateReg(deactivateCtx); err != nil {
			t.Logf("warning: failed to deactivate ephemeral staging account: %v", err)
		} else {
			t.Logf("Deactivated ephemeral Let's Encrypt Staging account: %s", account.URI)
		}
	}()

	// 2. Set up RouteController backed by the live Let's Encrypt Staging issuer
	rc, routeClient, kubeInf, routeInf := newTestRouteController()
	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "letsencrypt-staging",
			Namespace: "default",
		},
		Data: map[string]string{
			"cert-issuer.types.acme.openshift.io": `type: ACME
secretName: letsencrypt-staging
acmeCertIssuer:
  directoryURL: ` + stagingDirectoryURL,
		},
	}
	_ = kubeInf.InformersForOrGlobal("default").Core().V1().ConfigMaps().Informer().GetIndexer().Add(cm)

	keyPem := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(accountKey)})
	secret := &corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "letsencrypt-staging",
			Namespace: "default",
		},
		Data: map[string][]byte{
			corev1.TLSPrivateKeyKey: keyPem,
		},
	}
	_ = kubeInf.InformersForOrGlobal("default").Core().V1().Secrets().Informer().GetIndexer().Add(secret)

	profiles := []struct {
		name        string
		profile     string
		expectError string
	}{
		{name: "default (no profile specified)", profile: ""},
		{name: "classic profile (64-day)", profile: "classic"},
		{name: "tlsserver profile (45-day)", profile: "tlsserver"},
		{name: "shortlived profile (160-hour)", profile: "shortlived"},
		{name: "invalid profile rejected via live directory check", profile: "invalid-profile-name", expectError: "does not advertise profile"},
	}

	for i, tc := range profiles {
		t.Run(tc.name, func(t *testing.T) {
			routeName := fmt.Sprintf("staging-route-%d", i)
			// Use a syntactically valid public-TLD domain for ACME order creation
			domain := fmt.Sprintf("ci-%s-%d.openshift-acme-test.com", utilrand.String(8), i)

			annotations := map[string]string{
				"kubernetes.io/tls-acme":             "true",
				"acme.openshift.io/cert-issuer-name": "letsencrypt-staging",
			}
			if tc.profile != "" {
				annotations[api.AcmeCertProfile] = tc.profile
			}

			route := &routev1.Route{
				ObjectMeta: metav1.ObjectMeta{
					Name:        routeName,
					Namespace:   "default",
					Annotations: annotations,
				},
				Spec: routev1.RouteSpec{
					Host: domain,
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
			_, _ = routeClient.RouteV1().Routes("default").Create(ctx, route, metav1.CreateOptions{})
			_ = routeInf.InformersForOrGlobal("default").Route().V1().Routes().Informer().GetIndexer().Add(route)

			syncErr := rc.sync(ctx, "default/"+routeName)
			if tc.expectError != "" {
				if syncErr == nil || !strings.Contains(syncErr.Error(), tc.expectError) {
					t.Fatalf("expected error containing %q, got: %v", tc.expectError, syncErr)
				}
				return
			}
			if syncErr != nil {
				t.Fatalf("sync against Let's Encrypt Staging failed for profile %q: %v", tc.profile, syncErr)
			}

			updatedRoute, err := routeClient.RouteV1().Routes("default").Get(ctx, routeName, metav1.GetOptions{})
			if err != nil {
				t.Fatalf("failed to get updated Route: %v", err)
			}
			status, err := rc.getStatus(updatedRoute)
			if err != nil {
				t.Fatalf("failed to decode Route status: %v", err)
			}
			if status.ProvisioningStatus.OrderURI == "" {
				t.Fatalf("expected non-empty OrderURI from Let's Encrypt Staging, got empty")
			}
			if status.ProvisioningStatus.OrderStatus != acme.StatusPending {
				t.Errorf("expected OrderStatus %q, got %q", acme.StatusPending, status.ProvisioningStatus.OrderStatus)
			}

			// Fetch the order from Let's Encrypt Staging and inspect the raw JSON response
			// to verify Boulder recorded the expected certificate profile.
			capturingRT.lastBody = nil
			order, err := client.GetOrder(ctx, status.ProvisioningStatus.OrderURI)
			if err != nil {
				t.Fatalf("failed to fetch created order %q from Let's Encrypt Staging: %v", status.ProvisioningStatus.OrderURI, err)
			}
			var rawOrder struct {
				Profile string `json:"profile"`
			}
			if err := json.Unmarshal(capturingRT.lastBody, &rawOrder); err != nil {
				t.Fatalf("failed to unmarshal raw order response from Let's Encrypt Staging: %v", err)
			}
			if rawOrder.Profile != tc.profile {
				t.Errorf("expected Let's Encrypt Staging order profile %q, got %q", tc.profile, rawOrder.Profile)
			}
			t.Logf("Profile %q -> OrderURI=%s RecordedProfile=%s Status=%s AuthzCount=%d Expires=%s",
				tc.profile, status.ProvisioningStatus.OrderURI, rawOrder.Profile, order.Status, len(order.AuthzURLs), order.Expires.Format(time.RFC3339))
		})
	}
}

func TestSync_ReadyOrderPersistsStatusAndResetsFailures(t *testing.T) {
	now := time.Now()
	issuedCert := generateTestCertificate(t, []string{"example.com"}, now.Add(-1*time.Hour), now.Add(64*24*time.Hour))

	var orderQueries int
	var ts *httptest.Server
	ts = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Replay-Nonce", "test-nonce")
		switch r.URL.Path {
		case "/directory":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{
				"newNonce": %q,
				"newAccount": %q,
				"newOrder": %q
			}`, ts.URL+"/new-nonce", ts.URL+"/new-account", ts.URL+"/new-order")
		case "/new-nonce":
			w.WriteHeader(http.StatusNoContent)
		case "/order/1":
			orderQueries++
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"status":"ready","finalize":%q}`, ts.URL+"/finalize/1")
		case "/finalize/1":
			w.Header().Set("Content-Type", "application/json")
			_, _ = fmt.Fprintf(w, `{"status":"valid","certificate":%q}`, ts.URL+"/cert/1")
		case "/cert/1":
			w.Header().Set("Content-Type", "application/pem-certificate-chain")
			_, _ = w.Write(issuedCert.Crt)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer ts.Close()

	rc, routeClient, kubeInf, routeInf := newTestRouteController()
	recorderQueue := &recordingRateLimitingQueue{
		TypedRateLimitingInterface: rc.queue,
		addedAfter:                 make(map[string]time.Duration),
	}
	rc.queue = recorderQueue

	cm := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "test-issuer",
			Namespace: "default",
		},
		Data: map[string]string{
			"cert-issuer.types.acme.openshift.io": `type: ACME
secretName: letsencrypt-live
acmeCertIssuer:
  directoryURL: ` + ts.URL + `/directory`,
		},
	}
	_ = kubeInf.InformersForOrGlobal("default").Core().V1().ConfigMaps().Informer().GetIndexer().Add(cm)

	key, err := rsa.GenerateKey(cryptorand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
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
				api.AcmeStatusAnnotation:             `{"provisioningStatus":{"orderURI":"` + ts.URL + `/order/1","orderStatus":"ready","failures":3}}`,
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
	_, _ = routeClient.RouteV1().Routes("default").Create(context.Background(), route, metav1.CreateOptions{})
	_ = routeInf.InformersForOrGlobal("default").Route().V1().Routes().Informer().GetIndexer().Add(route)

	// 1. First sync finalizes the ready order and persists both TLS cert/key and OrderStatus=valid (Failures=0)
	if err := rc.sync(context.Background(), "default/test-route"); err != nil {
		t.Fatalf("first sync failed: %v", err)
	}

	updatedRoute, err := routeClient.RouteV1().Routes("default").Get(context.Background(), "test-route", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get updated Route: %v", err)
	}
	if updatedRoute.Spec.TLS == nil || updatedRoute.Spec.TLS.Certificate == "" || updatedRoute.Spec.TLS.Key == "" {
		t.Fatalf("expected Route TLS certificate and key to be populated, got: %+v", updatedRoute.Spec.TLS)
	}
	statusAfterCert, err := rc.getStatus(updatedRoute)
	if err != nil {
		t.Fatalf("failed to decode status after cert issuance: %v", err)
	}
	if statusAfterCert.ProvisioningStatus.OrderStatus != acme.StatusValid {
		t.Errorf("expected OrderStatus %q to be persisted with certificate update, got %q", acme.StatusValid, statusAfterCert.ProvisioningStatus.OrderStatus)
	}
	if statusAfterCert.ProvisioningStatus.Failures != 0 {
		t.Errorf("expected Failures to be reset to 0 after successful issuance, got %d", statusAfterCert.ProvisioningStatus.Failures)
	}

	// 2. Subsequent sync (triggered by the Route Update watch event) must NOT re-query the ACME
	// order or hit the orphaned-Valid-order path; it should clear OrderURI/OrderStatus and schedule
	// the renewal requeue.
	_ = routeInf.InformersForOrGlobal("default").Route().V1().Routes().Informer().GetIndexer().Update(updatedRoute)
	orderQueriesBeforeSecondSync := orderQueries
	if err := rc.sync(context.Background(), "default/test-route"); err != nil {
		t.Fatalf("second sync failed: %v", err)
	}
	if orderQueries != orderQueriesBeforeSecondSync {
		t.Errorf("expected second sync not to query ACME order endpoint, but orderQueries increased from %d to %d", orderQueriesBeforeSecondSync, orderQueries)
	}
	if _, ok := recorderQueue.addedAfter["default/test-route"]; !ok {
		t.Errorf("expected second sync to schedule deterministic renewal requeue via queue.AddAfter")
	}

	finalRoute, err := routeClient.RouteV1().Routes("default").Get(context.Background(), "test-route", metav1.GetOptions{})
	if err != nil {
		t.Fatalf("failed to get final Route: %v", err)
	}
	finalStatus, err := rc.getStatus(finalRoute)
	if err != nil {
		t.Fatalf("failed to decode final status: %v", err)
	}
	if finalStatus.ProvisioningStatus.OrderURI != "" || finalStatus.ProvisioningStatus.OrderStatus != "" {
		t.Errorf("expected OrderURI and OrderStatus to be cleared on settled Route, got OrderURI=%q OrderStatus=%q",
			finalStatus.ProvisioningStatus.OrderURI, finalStatus.ProvisioningStatus.OrderStatus)
	}
	if finalStatus.ProvisioningStatus.Failures != 0 {
		t.Errorf("expected Failures to remain 0 on settled Route, got %d", finalStatus.ProvisioningStatus.Failures)
	}
}
