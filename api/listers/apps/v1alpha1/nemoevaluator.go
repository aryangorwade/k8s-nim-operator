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
// Code generated by lister-gen. DO NOT EDIT.

package v1alpha1

import (
	v1alpha1 "github.com/NVIDIA/k8s-nim-operator/api/apps/v1alpha1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/client-go/listers"
	"k8s.io/client-go/tools/cache"
)

// NemoEvaluatorLister helps list NemoEvaluators.
// All objects returned here must be treated as read-only.
type NemoEvaluatorLister interface {
	// List lists all NemoEvaluators in the indexer.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1alpha1.NemoEvaluator, err error)
	// NemoEvaluators returns an object that can list and get NemoEvaluators.
	NemoEvaluators(namespace string) NemoEvaluatorNamespaceLister
	NemoEvaluatorListerExpansion
}

// nemoEvaluatorLister implements the NemoEvaluatorLister interface.
type nemoEvaluatorLister struct {
	listers.ResourceIndexer[*v1alpha1.NemoEvaluator]
}

// NewNemoEvaluatorLister returns a new NemoEvaluatorLister.
func NewNemoEvaluatorLister(indexer cache.Indexer) NemoEvaluatorLister {
	return &nemoEvaluatorLister{listers.New[*v1alpha1.NemoEvaluator](indexer, v1alpha1.Resource("nemoevaluator"))}
}

// NemoEvaluators returns an object that can list and get NemoEvaluators.
func (s *nemoEvaluatorLister) NemoEvaluators(namespace string) NemoEvaluatorNamespaceLister {
	return nemoEvaluatorNamespaceLister{listers.NewNamespaced[*v1alpha1.NemoEvaluator](s.ResourceIndexer, namespace)}
}

// NemoEvaluatorNamespaceLister helps list and get NemoEvaluators.
// All objects returned here must be treated as read-only.
type NemoEvaluatorNamespaceLister interface {
	// List lists all NemoEvaluators in the indexer for a given namespace.
	// Objects returned here must be treated as read-only.
	List(selector labels.Selector) (ret []*v1alpha1.NemoEvaluator, err error)
	// Get retrieves the NemoEvaluator from the indexer for a given namespace and name.
	// Objects returned here must be treated as read-only.
	Get(name string) (*v1alpha1.NemoEvaluator, error)
	NemoEvaluatorNamespaceListerExpansion
}

// nemoEvaluatorNamespaceLister implements the NemoEvaluatorNamespaceLister
// interface.
type nemoEvaluatorNamespaceLister struct {
	listers.ResourceIndexer[*v1alpha1.NemoEvaluator]
}
