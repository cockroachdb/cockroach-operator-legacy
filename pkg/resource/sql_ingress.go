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

package resource

import (
	"errors"

	"github.com/cockroachdb/cockroach-operator/pkg/kube"
	v1 "k8s.io/api/networking/v1"
	"k8s.io/api/networking/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/intstr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// SQLIngressBuilder models the Ingress that the operator maintains.
type SQLIngressBuilder struct {
	*Cluster
	V1Ingress bool
	Labels    map[string]string
}

func (b SQLIngressBuilder) ResourceName() string {
	return "sql-" + b.IngressSuffix()
}

func (b SQLIngressBuilder) Build(obj client.Object) error {

	if b.V1Ingress {
		return b.BuildV1Ingress(obj)
	}

	return b.BuildV1beta1Ingress(obj)
}

func (b *SQLIngressBuilder) BuildV1Ingress(obj client.Object) error {
	ingress, ok := obj.(*v1.Ingress)
	if !ok {
		return errors.New("failed to cast to Ingress object")
	}

	if ingress.ObjectMeta.Name == "" {
		ingress.ObjectMeta.Name = b.ResourceName()
	}

	if ingress.ObjectMeta.Labels == nil {
		ingress.ObjectMeta.Labels = map[string]string{}
	}

	ingress.Labels = b.Labels
	ingress.Annotations = b.Spec().AdditionalAnnotations

	if ingress.ObjectMeta.Annotations == nil {
		ingress.ObjectMeta.Annotations = map[string]string{}
	}

	ingressConfig := b.Spec().Ingress

	if ingressConfig == nil {
		return errors.New("ingressConfig not found")
	}

	if ingress.ObjectMeta.Annotations == nil {
		ingress.ObjectMeta.Annotations = map[string]string{}
	}

	kube.MergeAnnotations(ingress.ObjectMeta.Annotations, ingressConfig.SQL.Annotations)

	ingress.Spec = v1.IngressSpec{
		Rules: []v1.IngressRule{
			getV1IngressRule(ingressConfig.SQL.Host, b.PublicServiceName(), intstr.FromString("sql")),
		},
	}

	if ingressConfig.SQL.IngressClassName != "" {
		ingress.Spec.IngressClassName = &ingressConfig.SQL.IngressClassName
	}

	for i := range ingressConfig.SQL.TLS {
		ingress.Spec.TLS = append(ingress.Spec.TLS, v1.IngressTLS{
			Hosts:      ingressConfig.SQL.TLS[i].Hosts,
			SecretName: ingressConfig.SQL.TLS[i].SecretName,
		})
	}

	return nil
}

func (b *SQLIngressBuilder) BuildV1beta1Ingress(obj client.Object) error {
	ingress, ok := obj.(*v1beta1.Ingress)
	if !ok {
		return errors.New("failed to cast to Ingress object")
	}

	if ingress.ObjectMeta.Name == "" {
		ingress.ObjectMeta.Name = b.ResourceName()
	}

	if ingress.ObjectMeta.Labels == nil {
		ingress.ObjectMeta.Labels = map[string]string{}
	}

	ingress.Labels = b.Labels
	ingress.Annotations = b.Spec().AdditionalAnnotations

	if ingress.ObjectMeta.Annotations == nil {
		ingress.ObjectMeta.Annotations = map[string]string{}
	}

	ingressConfig := b.Spec().Ingress

	if ingressConfig == nil {
		return errors.New("ingressConfig not fouund")
	}

	kube.MergeAnnotations(ingress.ObjectMeta.Annotations, ingressConfig.SQL.Annotations)

	ingress.Spec = v1beta1.IngressSpec{
		Rules: []v1beta1.IngressRule{
			getV1beta1IngressRule(ingressConfig.SQL.Host, b.PublicServiceName(), intstr.FromString("sql")),
		},
	}

	if ingressConfig.SQL.IngressClassName != "" {
		ingress.Spec.IngressClassName = &ingressConfig.SQL.IngressClassName
	}

	return nil
}

func (b SQLIngressBuilder) Placeholder() client.Object {
	objectMeta := metav1.ObjectMeta{
		Name: b.ResourceName(),
	}

	if b.V1Ingress {
		return &v1.Ingress{
			ObjectMeta: objectMeta,
		}
	}

	return &v1beta1.Ingress{
		ObjectMeta: objectMeta,
	}
}
