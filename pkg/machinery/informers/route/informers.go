// Package route provides an OpenShift Route informer factory scoped to one or
// more namespaces.
package route

import (
	"time"

	routeclientset "github.com/openshift/client-go/route/clientset/versioned"
	routeinformers "github.com/openshift/client-go/route/informers/externalversions"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DefaultResyncPeriod is the periodic resync interval for Route informers,
// acting as a safety net alongside explicit renewal requeues.
const DefaultResyncPeriod = 6 * time.Hour

type Interface interface {
	Start(stopCh <-chan struct{})
	InformersFor(namespace string) routeinformers.SharedInformerFactory
	InformersForOrGlobal(namespace string) routeinformers.SharedInformerFactory
	Namespaces() []string
}

type routeInformersForNamespaces map[string]routeinformers.SharedInformerFactory

var _ Interface = routeInformersForNamespaces{}

func NewRouteInformersForNamespaces(routeClient routeclientset.Interface, namespaces []string) routeInformersForNamespaces {
	res := routeInformersForNamespaces{}

	for _, namespace := range namespaces {
		res[namespace] = routeinformers.NewSharedInformerFactoryWithOptions(routeClient, DefaultResyncPeriod, routeinformers.WithNamespace(namespace))
	}

	return res
}

func (i routeInformersForNamespaces) Start(stopCh <-chan struct{}) {
	for _, informer := range i {
		informer.Start(stopCh)
	}
}

func (i routeInformersForNamespaces) Namespaces() []string {
	var ns []string
	for n := range i {
		ns = append(ns, n)
	}
	return ns
}

func (i routeInformersForNamespaces) InformersFor(namespace string) routeinformers.SharedInformerFactory {
	return i[namespace]
}

func (i routeInformersForNamespaces) InformersForOrGlobal(namespace string) routeinformers.SharedInformerFactory {
	informer, ok := i[namespace]
	if !ok {
		return i[metav1.NamespaceAll]
	}
	return informer
}

func (i routeInformersForNamespaces) HasInformersFor(namespace string) bool {
	return i.InformersFor(namespace) != nil
}
