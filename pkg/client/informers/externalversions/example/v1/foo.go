/*
Copyright The Symphony Authors.

*/

// Code generated by informer-gen. DO NOT EDIT.

package v1

import (
	time "time"

	examplev1 "github.com/yangyongzhi/sym-operator/pkg/apis/example/v1"
	versioned "github.com/yangyongzhi/sym-operator/pkg/client/clientset/versioned"
	internalinterfaces "github.com/yangyongzhi/sym-operator/pkg/client/informers/externalversions/internalinterfaces"
	v1 "github.com/yangyongzhi/sym-operator/pkg/client/listers/example/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"
	watch "k8s.io/apimachinery/pkg/watch"
	cache "k8s.io/client-go/tools/cache"
)

// FooInformer provides access to a shared informer and lister for
// Foos.
type FooInformer interface {
	Informer() cache.SharedIndexInformer
	Lister() v1.FooLister
}

type fooInformer struct {
	factory          internalinterfaces.SharedInformerFactory
	tweakListOptions internalinterfaces.TweakListOptionsFunc
	namespace        string
}

// NewFooInformer constructs a new informer for Foo type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFooInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers) cache.SharedIndexInformer {
	return NewFilteredFooInformer(client, namespace, resyncPeriod, indexers, nil)
}

// NewFilteredFooInformer constructs a new informer for Foo type.
// Always prefer using an informer factory to get a shared informer instead of getting an independent
// one. This reduces memory footprint and number of connections to the server.
func NewFilteredFooInformer(client versioned.Interface, namespace string, resyncPeriod time.Duration, indexers cache.Indexers, tweakListOptions internalinterfaces.TweakListOptionsFunc) cache.SharedIndexInformer {
	return cache.NewSharedIndexInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ExampleV1().Foos(namespace).List(options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				if tweakListOptions != nil {
					tweakListOptions(&options)
				}
				return client.ExampleV1().Foos(namespace).Watch(options)
			},
		},
		&examplev1.Foo{},
		resyncPeriod,
		indexers,
	)
}

func (f *fooInformer) defaultInformer(client versioned.Interface, resyncPeriod time.Duration) cache.SharedIndexInformer {
	return NewFilteredFooInformer(client, f.namespace, resyncPeriod, cache.Indexers{cache.NamespaceIndex: cache.MetaNamespaceIndexFunc}, f.tweakListOptions)
}

func (f *fooInformer) Informer() cache.SharedIndexInformer {
	return f.factory.InformerFor(&examplev1.Foo{}, f.defaultInformer)
}

func (f *fooInformer) Lister() v1.FooLister {
	return v1.NewFooLister(f.Informer().GetIndexer())
}