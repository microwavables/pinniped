// Copyright 2020 the Pinniped contributors. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Code generated by client-gen. DO NOT EDIT.

package v1alpha1

import (
	v1alpha1 "github.com/vmware-tanzu/pinniped/generated/1.17/apis/pinniped/v1alpha1"
	"github.com/vmware-tanzu/pinniped/generated/1.17/client/clientset/versioned/scheme"
	rest "k8s.io/client-go/rest"
)

type PinnipedV1alpha1Interface interface {
	RESTClient() rest.Interface
	CredentialRequestsGetter
}

// PinnipedV1alpha1Client is used to interact with features provided by the pinniped.dev group.
type PinnipedV1alpha1Client struct {
	restClient rest.Interface
}

func (c *PinnipedV1alpha1Client) CredentialRequests() CredentialRequestInterface {
	return newCredentialRequests(c)
}

// NewForConfig creates a new PinnipedV1alpha1Client for the given config.
func NewForConfig(c *rest.Config) (*PinnipedV1alpha1Client, error) {
	config := *c
	if err := setConfigDefaults(&config); err != nil {
		return nil, err
	}
	client, err := rest.RESTClientFor(&config)
	if err != nil {
		return nil, err
	}
	return &PinnipedV1alpha1Client{client}, nil
}

// NewForConfigOrDie creates a new PinnipedV1alpha1Client for the given config and
// panics if there is an error in the config.
func NewForConfigOrDie(c *rest.Config) *PinnipedV1alpha1Client {
	client, err := NewForConfig(c)
	if err != nil {
		panic(err)
	}
	return client
}

// New creates a new PinnipedV1alpha1Client for the given RESTClient.
func New(c rest.Interface) *PinnipedV1alpha1Client {
	return &PinnipedV1alpha1Client{c}
}

func setConfigDefaults(config *rest.Config) error {
	gv := v1alpha1.SchemeGroupVersion
	config.GroupVersion = &gv
	config.APIPath = "/apis"
	config.NegotiatedSerializer = scheme.Codecs.WithoutConversion()

	if config.UserAgent == "" {
		config.UserAgent = rest.DefaultKubernetesUserAgent()
	}

	return nil
}

// RESTClient returns a RESTClient that is used to communicate
// with API server by this client implementation.
func (c *PinnipedV1alpha1Client) RESTClient() rest.Interface {
	if c == nil {
		return nil
	}
	return c.restClient
}
