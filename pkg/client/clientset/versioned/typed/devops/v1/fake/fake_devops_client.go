/*
Copyright The Symphony Authors.

*/

// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	v1 "github.com/yangyongzhi/sym-operator/pkg/client/clientset/versioned/typed/devops/v1"
	rest "k8s.io/client-go/rest"
	testing "k8s.io/client-go/testing"
)

type FakeDevopsV1 struct {
	*testing.Fake
}

func (c *FakeDevopsV1) Migrates(namespace string) v1.MigrateInterface {
	return &FakeMigrates{c, namespace}
}

// RESTClient returns a RESTClient that is used to communicate
// with API server by this client implementation.
func (c *FakeDevopsV1) RESTClient() rest.Interface {
	var ret *rest.RESTClient
	return ret
}