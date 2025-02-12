/*
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	v1alpha1 "github.com/sap/valkey-operator/pkg/client/clientset/versioned/typed/cache.cs.sap.com/v1alpha1"
	rest "k8s.io/client-go/rest"
	testing "k8s.io/client-go/testing"
)

type FakeCacheV1alpha1 struct {
	*testing.Fake
}

func (c *FakeCacheV1alpha1) Valkey(namespace string) v1alpha1.ValkeyInterface {
	return newFakeValkey(c, namespace)
}

// RESTClient returns a RESTClient that is used to communicate
// with API server by this client implementation.
func (c *FakeCacheV1alpha1) RESTClient() rest.Interface {
	var ret *rest.RESTClient
	return ret
}
