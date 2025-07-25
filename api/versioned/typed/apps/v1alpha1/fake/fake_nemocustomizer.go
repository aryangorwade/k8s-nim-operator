/*
Copyright 2025.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/
// Code generated by client-gen. DO NOT EDIT.

package fake

import (
	"context"

	v1alpha1 "github.com/NVIDIA/k8s-nim-operator/api/apps/v1alpha1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	labels "k8s.io/apimachinery/pkg/labels"
	types "k8s.io/apimachinery/pkg/types"
	watch "k8s.io/apimachinery/pkg/watch"
	testing "k8s.io/client-go/testing"
)

// FakeNemoCustomizers implements NemoCustomizerInterface
type FakeNemoCustomizers struct {
	Fake *FakeAppsV1alpha1
	ns   string
}

var nemocustomizersResource = v1alpha1.SchemeGroupVersion.WithResource("nemocustomizers")

var nemocustomizersKind = v1alpha1.SchemeGroupVersion.WithKind("NemoCustomizer")

// Get takes name of the nemoCustomizer, and returns the corresponding nemoCustomizer object, and an error if there is any.
func (c *FakeNemoCustomizers) Get(ctx context.Context, name string, options v1.GetOptions) (result *v1alpha1.NemoCustomizer, err error) {
	emptyResult := &v1alpha1.NemoCustomizer{}
	obj, err := c.Fake.
		Invokes(testing.NewGetActionWithOptions(nemocustomizersResource, c.ns, name, options), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.NemoCustomizer), err
}

// List takes label and field selectors, and returns the list of NemoCustomizers that match those selectors.
func (c *FakeNemoCustomizers) List(ctx context.Context, opts v1.ListOptions) (result *v1alpha1.NemoCustomizerList, err error) {
	emptyResult := &v1alpha1.NemoCustomizerList{}
	obj, err := c.Fake.
		Invokes(testing.NewListActionWithOptions(nemocustomizersResource, nemocustomizersKind, c.ns, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}

	label, _, _ := testing.ExtractFromListOptions(opts)
	if label == nil {
		label = labels.Everything()
	}
	list := &v1alpha1.NemoCustomizerList{ListMeta: obj.(*v1alpha1.NemoCustomizerList).ListMeta}
	for _, item := range obj.(*v1alpha1.NemoCustomizerList).Items {
		if label.Matches(labels.Set(item.Labels)) {
			list.Items = append(list.Items, item)
		}
	}
	return list, err
}

// Watch returns a watch.Interface that watches the requested nemoCustomizers.
func (c *FakeNemoCustomizers) Watch(ctx context.Context, opts v1.ListOptions) (watch.Interface, error) {
	return c.Fake.
		InvokesWatch(testing.NewWatchActionWithOptions(nemocustomizersResource, c.ns, opts))

}

// Create takes the representation of a nemoCustomizer and creates it.  Returns the server's representation of the nemoCustomizer, and an error, if there is any.
func (c *FakeNemoCustomizers) Create(ctx context.Context, nemoCustomizer *v1alpha1.NemoCustomizer, opts v1.CreateOptions) (result *v1alpha1.NemoCustomizer, err error) {
	emptyResult := &v1alpha1.NemoCustomizer{}
	obj, err := c.Fake.
		Invokes(testing.NewCreateActionWithOptions(nemocustomizersResource, c.ns, nemoCustomizer, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.NemoCustomizer), err
}

// Update takes the representation of a nemoCustomizer and updates it. Returns the server's representation of the nemoCustomizer, and an error, if there is any.
func (c *FakeNemoCustomizers) Update(ctx context.Context, nemoCustomizer *v1alpha1.NemoCustomizer, opts v1.UpdateOptions) (result *v1alpha1.NemoCustomizer, err error) {
	emptyResult := &v1alpha1.NemoCustomizer{}
	obj, err := c.Fake.
		Invokes(testing.NewUpdateActionWithOptions(nemocustomizersResource, c.ns, nemoCustomizer, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.NemoCustomizer), err
}

// UpdateStatus was generated because the type contains a Status member.
// Add a +genclient:noStatus comment above the type to avoid generating UpdateStatus().
func (c *FakeNemoCustomizers) UpdateStatus(ctx context.Context, nemoCustomizer *v1alpha1.NemoCustomizer, opts v1.UpdateOptions) (result *v1alpha1.NemoCustomizer, err error) {
	emptyResult := &v1alpha1.NemoCustomizer{}
	obj, err := c.Fake.
		Invokes(testing.NewUpdateSubresourceActionWithOptions(nemocustomizersResource, "status", c.ns, nemoCustomizer, opts), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.NemoCustomizer), err
}

// Delete takes name of the nemoCustomizer and deletes it. Returns an error if one occurs.
func (c *FakeNemoCustomizers) Delete(ctx context.Context, name string, opts v1.DeleteOptions) error {
	_, err := c.Fake.
		Invokes(testing.NewDeleteActionWithOptions(nemocustomizersResource, c.ns, name, opts), &v1alpha1.NemoCustomizer{})

	return err
}

// DeleteCollection deletes a collection of objects.
func (c *FakeNemoCustomizers) DeleteCollection(ctx context.Context, opts v1.DeleteOptions, listOpts v1.ListOptions) error {
	action := testing.NewDeleteCollectionActionWithOptions(nemocustomizersResource, c.ns, opts, listOpts)

	_, err := c.Fake.Invokes(action, &v1alpha1.NemoCustomizerList{})
	return err
}

// Patch applies the patch and returns the patched nemoCustomizer.
func (c *FakeNemoCustomizers) Patch(ctx context.Context, name string, pt types.PatchType, data []byte, opts v1.PatchOptions, subresources ...string) (result *v1alpha1.NemoCustomizer, err error) {
	emptyResult := &v1alpha1.NemoCustomizer{}
	obj, err := c.Fake.
		Invokes(testing.NewPatchSubresourceActionWithOptions(nemocustomizersResource, c.ns, name, pt, data, opts, subresources...), emptyResult)

	if obj == nil {
		return emptyResult, err
	}
	return obj.(*v1alpha1.NemoCustomizer), err
}
