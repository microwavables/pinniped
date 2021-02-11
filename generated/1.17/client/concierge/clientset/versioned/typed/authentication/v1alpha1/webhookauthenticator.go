// Copyright 2020-2021 the Pinniped contributors. All Rights Reserved.
// SPDX-License-Identifier: Apache-2.0

// Code generated by client-gen. DO NOT EDIT.

package v1alpha1

import (
	"time"

	v1alpha1 "go.pinniped.dev/generated/1.17/apis/concierge/authentication/v1alpha1"
	scheme "go.pinniped.dev/generated/1.17/client/concierge/clientset/versioned/scheme"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	rest "k8s.io/client-go/rest"
)

// WebhookAuthenticatorsGetter has a method to return a WebhookAuthenticatorInterface.
// A group's client should implement this interface.
type WebhookAuthenticatorsGetter interface {
	WebhookAuthenticators() WebhookAuthenticatorInterface
}

// WebhookAuthenticatorInterface has methods to work with WebhookAuthenticator resources.
type WebhookAuthenticatorInterface interface {
	Create(*v1alpha1.WebhookAuthenticator) (*v1alpha1.WebhookAuthenticator, error)
	Update(*v1alpha1.WebhookAuthenticator) (*v1alpha1.WebhookAuthenticator, error)
	UpdateStatus(*v1alpha1.WebhookAuthenticator) (*v1alpha1.WebhookAuthenticator, error)
	Delete(name string, options *v1.DeleteOptions) error
	DeleteCollection(options *v1.DeleteOptions, listOptions v1.ListOptions) error
	Get(name string, options v1.GetOptions) (*v1alpha1.WebhookAuthenticator, error)
	List(opts v1.ListOptions) (*v1alpha1.WebhookAuthenticatorList, error)
	Watch(opts v1.ListOptions) (watch.Interface, error)
	Patch(name string, pt types.PatchType, data []byte, subresources ...string) (result *v1alpha1.WebhookAuthenticator, err error)
	WebhookAuthenticatorExpansion
}

// webhookAuthenticators implements WebhookAuthenticatorInterface
type webhookAuthenticators struct {
	client rest.Interface
}

// newWebhookAuthenticators returns a WebhookAuthenticators
func newWebhookAuthenticators(c *AuthenticationV1alpha1Client) *webhookAuthenticators {
	return &webhookAuthenticators{
		client: c.RESTClient(),
	}
}

// Get takes name of the webhookAuthenticator, and returns the corresponding webhookAuthenticator object, and an error if there is any.
func (c *webhookAuthenticators) Get(name string, options v1.GetOptions) (result *v1alpha1.WebhookAuthenticator, err error) {
	result = &v1alpha1.WebhookAuthenticator{}
	err = c.client.Get().
		Resource("webhookauthenticators").
		Name(name).
		VersionedParams(&options, scheme.ParameterCodec).
		Do().
		Into(result)
	return
}

// List takes label and field selectors, and returns the list of WebhookAuthenticators that match those selectors.
func (c *webhookAuthenticators) List(opts v1.ListOptions) (result *v1alpha1.WebhookAuthenticatorList, err error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	result = &v1alpha1.WebhookAuthenticatorList{}
	err = c.client.Get().
		Resource("webhookauthenticators").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		Do().
		Into(result)
	return
}

// Watch returns a watch.Interface that watches the requested webhookAuthenticators.
func (c *webhookAuthenticators) Watch(opts v1.ListOptions) (watch.Interface, error) {
	var timeout time.Duration
	if opts.TimeoutSeconds != nil {
		timeout = time.Duration(*opts.TimeoutSeconds) * time.Second
	}
	opts.Watch = true
	return c.client.Get().
		Resource("webhookauthenticators").
		VersionedParams(&opts, scheme.ParameterCodec).
		Timeout(timeout).
		Watch()
}

// Create takes the representation of a webhookAuthenticator and creates it.  Returns the server's representation of the webhookAuthenticator, and an error, if there is any.
func (c *webhookAuthenticators) Create(webhookAuthenticator *v1alpha1.WebhookAuthenticator) (result *v1alpha1.WebhookAuthenticator, err error) {
	result = &v1alpha1.WebhookAuthenticator{}
	err = c.client.Post().
		Resource("webhookauthenticators").
		Body(webhookAuthenticator).
		Do().
		Into(result)
	return
}

// Update takes the representation of a webhookAuthenticator and updates it. Returns the server's representation of the webhookAuthenticator, and an error, if there is any.
func (c *webhookAuthenticators) Update(webhookAuthenticator *v1alpha1.WebhookAuthenticator) (result *v1alpha1.WebhookAuthenticator, err error) {
	result = &v1alpha1.WebhookAuthenticator{}
	err = c.client.Put().
		Resource("webhookauthenticators").
		Name(webhookAuthenticator.Name).
		Body(webhookAuthenticator).
		Do().
		Into(result)
	return
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().

func (c *webhookAuthenticators) UpdateStatus(webhookAuthenticator *v1alpha1.WebhookAuthenticator) (result *v1alpha1.WebhookAuthenticator, err error) {
	result = &v1alpha1.WebhookAuthenticator{}
	err = c.client.Put().
		Resource("webhookauthenticators").
		Name(webhookAuthenticator.Name).
		SubResource("status").
		Body(webhookAuthenticator).
		Do().
		Into(result)
	return
}

// Delete takes name of the webhookAuthenticator and deletes it. Returns an error if one occurs.
func (c *webhookAuthenticators) Delete(name string, options *v1.DeleteOptions) error {
	return c.client.Delete().
		Resource("webhookauthenticators").
		Name(name).
		Body(options).
		Do().
		Error()
}

// DeleteCollection deletes a collection of objects.
func (c *webhookAuthenticators) DeleteCollection(options *v1.DeleteOptions, listOptions v1.ListOptions) error {
	var timeout time.Duration
	if listOptions.TimeoutSeconds != nil {
		timeout = time.Duration(*listOptions.TimeoutSeconds) * time.Second
	}
	return c.client.Delete().
		Resource("webhookauthenticators").
		VersionedParams(&listOptions, scheme.ParameterCodec).
		Timeout(timeout).
		Body(options).
		Do().
		Error()
}

// Patch applies the patch and returns the patched webhookAuthenticator.
func (c *webhookAuthenticators) Patch(name string, pt types.PatchType, data []byte, subresources ...string) (result *v1alpha1.WebhookAuthenticator, err error) {
	result = &v1alpha1.WebhookAuthenticator{}
	err = c.client.Patch(pt).
		Resource("webhookauthenticators").
		SubResource(subresources...).
		Name(name).
		Body(data).
		Do().
		Into(result)
	return
}
