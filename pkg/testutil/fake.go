/*
Copyright 2026 The Cockroach Authors

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    https://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package testutil

import (
	"context"

	api "github.com/cockroachdb/cockroach-operator/apis/v1alpha1"
	"github.com/cockroachdb/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func NewFakeClient(scheme *runtime.Scheme, objs ...runtime.Object) *FakeClient {
	return &FakeClient{
		client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objs...).
			WithStatusSubresource(&api.CrdbCluster{}).Build(),
		scheme: scheme,
	}
}

var _ client.Client = &FakeClient{}

// FakeClient is inspired and taken from https://github.com/kubernetes/client-go/
type FakeClient struct {
	client client.Client
	scheme *runtime.Scheme

	// ReactionChain is the list of reactors that will be attempted for every
	// request in the order they are tried.
	ReactionChain []Reactor
}

// Reactor is an interface to allow the composition of reaction functions.
type Reactor interface {
	// Handles indicates whether or not this Reactor deals with a given
	// action.
	Handles(action Action) bool
	// React handles the action and returns results.  It may choose to
	// delegate by indicated handled=false.
	React(action Action) (handled bool, err error)
}

// ReactionFunc is a function returning `handled` set to true if no further
// processing is required and an error if one needs to be raised
type ReactionFunc func(action Action) (handled bool, err error)

func NewGetAction(key client.ObjectKey, gvr schema.GroupVersionResource) Action {
	return &GetAction{
		verb: "get",
		key:  key,
		gvr:  gvr,
	}
}

func NewCreateAction(key client.ObjectKey, gvr schema.GroupVersionResource) Action {
	return &GetAction{
		verb: "create",
		key:  key,
		gvr:  gvr,
	}
}

func NewUpdateAction(key client.ObjectKey, gvr schema.GroupVersionResource) Action {
	return &GetAction{
		verb: "update",
		key:  key,
		gvr:  gvr,
	}
}

type Action interface {
	Verb() string
	GVR() schema.GroupVersionResource
	Key() client.ObjectKey
}

var _ Action = &GetAction{}

type GetAction struct {
	verb string
	key  client.ObjectKey
	gvr  schema.GroupVersionResource
}

type CreateAction struct {
	verb string
	key  client.ObjectKey
	gvr  schema.GroupVersionResource
}

var _ Reactor = &simpleReactor{}

type simpleReactor struct {
	Verb     string
	Resource string
	Reaction ReactionFunc
}

func (r simpleReactor) Handles(action Action) bool {
	verbCovers := r.Verb == "*" || r.Verb == action.Verb()
	if !verbCovers {
		return false
	}

	resourceCovers := r.Resource == "*" || r.Resource == action.GVR().Resource

	return resourceCovers
}

func (r simpleReactor) React(action Action) (handled bool, err error) {
	return r.Reaction(action)
}

func (a GetAction) Verb() string {
	return a.verb
}

func (a GetAction) Key() client.ObjectKey {
	return a.key
}

func (a GetAction) GVR() schema.GroupVersionResource {
	return a.gvr
}

func (a CreateAction) Verb() string {
	return a.verb
}

func (a CreateAction) Key() client.ObjectKey {
	return a.key
}

func (a CreateAction) GVR() schema.GroupVersionResource {
	return a.gvr
}

func (c *FakeClient) AddReactor(verb string, resource string, reaction ReactionFunc) {
	c.ReactionChain = append(c.ReactionChain, &simpleReactor{verb, resource, reaction})
}

func (c *FakeClient) Get(ctx context.Context, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
	gvr, err := getGVRFromObject(c.scheme, obj)
	if err != nil {
		return errors.Wrapf(err, "failed to find GVR of object %s", key.String())
	}

	a := NewGetAction(key, gvr)

	if handled, err := c.invoke(a); handled {
		return err
	}

	gvk := obj.GetObjectKind().GroupVersionKind()

	err = c.client.Get(ctx, key, obj)
	if err != nil {
		return err
	}

	obj.GetObjectKind().SetGroupVersionKind(gvk)
	return nil
}

func (c *FakeClient) List(_ context.Context, list client.ObjectList, opts ...client.ListOption) error {
	panic("implement me")
}

func (c *FakeClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	gvr, err := getGVRFromObject(c.scheme, obj)
	if err != nil {
		return errors.Wrapf(err, "failed to find GVR of object")
	}

	key := client.ObjectKeyFromObject(obj)
	a := NewCreateAction(key, gvr)

	if handled, err := c.invoke(a); handled {
		return err
	}

	return c.client.Create(ctx, obj, opts...)
}

func (c *FakeClient) Delete(_ context.Context, obj client.Object, opts ...client.DeleteOption) error {
	panic("implement me")
}

func (c *FakeClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	gvr, err := getGVRFromObject(c.scheme, obj)
	if err != nil {
		return errors.Wrapf(err, "failed to find GVR of object")
	}

	key := client.ObjectKeyFromObject(obj)
	a := NewUpdateAction(key, gvr)

	if handled, err := c.invoke(a); handled {
		return err
	}

	return c.client.Update(ctx, obj, opts...)
}

func (c *FakeClient) Patch(_ context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	panic("implement me")
}

func (c *FakeClient) DeleteAllOf(_ context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	panic("implement me")
}

// Status writes go straight to the embedded controller-runtime fake. Only Get,
// Create and Update consult ReactionChain, so reactors cannot inject
// status-write errors.
func (c *FakeClient) Status() client.StatusWriter {
	return c.client.Status()
}

func (c *FakeClient) RESTMapper() meta.RESTMapper {
	return nil
}

func (c *FakeClient) Scheme() *runtime.Scheme {
	return c.scheme
}

func (c *FakeClient) invoke(action Action) (handled bool, err error) {
	for _, reactor := range c.ReactionChain {
		if !reactor.Handles(action) {
			continue
		}

		handled, err := reactor.React(action)
		if !handled {
			continue
		}

		return true, err
	}

	return false, nil
}

func getGVRFromObject(scheme *runtime.Scheme, obj runtime.Object) (schema.GroupVersionResource, error) {
	gvk, err := apiutil.GVKForObject(obj, scheme)
	if err != nil {
		return schema.GroupVersionResource{}, err
	}
	gvr, _ := meta.UnsafeGuessKindToResource(gvk)
	return gvr, nil
}

func (c *FakeClient) SubResource(subResource string) client.SubResourceClient {
	panic("implement me")
}

func (c *FakeClient) GroupVersionKindFor(obj runtime.Object) (schema.GroupVersionKind, error) {
	return apiutil.GVKForObject(obj, c.scheme)
}

func (c *FakeClient) IsObjectNamespaced(obj runtime.Object) (bool, error) {
	restMapper := c.RESTMapper()
	if restMapper == nil {
		return false, errors.New("RESTMapper is not implemented")
	}
	return apiutil.IsObjectNamespaced(obj, c.scheme, restMapper)
}
