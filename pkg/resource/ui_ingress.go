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

// UIIngressBuilder models the Ingress that the operator maintains.
type UIIngressBuilder struct {
	*Cluster
	V1Ingress bool
	Labels    map[string]string
}

func (b UIIngressBuilder) ResourceName() string {
	return "ui-" + b.IngressSuffix()
}

func (b UIIngressBuilder) Build(obj client.Object) error {

	if b.V1Ingress {
		return b.BuildV1Ingress(obj)
	}

	return b.BuildV1beta1Ingress(obj)
}

func (b *UIIngressBuilder) BuildV1Ingress(obj client.Object) error {
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

	kube.MergeAnnotations(ingress.ObjectMeta.Annotations, b.SSLPassThroughAnnotations())
	if b.Spec().Ingress.UI.TLS != nil {
		kube.MergeAnnotations(ingress.ObjectMeta.Annotations, b.BackendProtocolAnnotations())
	}
	ingressConfig := b.Spec().Ingress

	if ingressConfig == nil {
		return errors.New("ingressConfig not fouund")
	}

	kube.MergeAnnotations(ingress.ObjectMeta.Annotations, ingressConfig.UI.Annotations)

	ingress.Spec = v1.IngressSpec{
		TLS: ingressConfig.UI.TLS,
		Rules: []v1.IngressRule{
			getV1IngressRule(ingressConfig.UI.Host, b.PublicServiceName(), intstr.FromString("http")),
		},
	}

	if ingressConfig.UI.IngressClassName != "" {
		ingress.Spec.IngressClassName = &ingressConfig.UI.IngressClassName
	}

	return nil
}

func (b *UIIngressBuilder) BuildV1beta1Ingress(obj client.Object) error {
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

	kube.MergeAnnotations(ingress.ObjectMeta.Annotations, b.SSLPassThroughAnnotations())
	if b.Spec().Ingress.UI.TLS != nil {
		kube.MergeAnnotations(ingress.ObjectMeta.Annotations, b.BackendProtocolAnnotations())
	}
	ingressConfig := b.Spec().Ingress

	if ingressConfig == nil {
		return errors.New("ingressConfig not found")
	}

	kube.MergeAnnotations(ingress.ObjectMeta.Annotations, ingressConfig.UI.Annotations)

	ingress.Spec = v1beta1.IngressSpec{
		Rules: []v1beta1.IngressRule{
			getV1beta1IngressRule(ingressConfig.UI.Host, b.PublicServiceName(), intstr.FromString("http")),
		},
	}

	if ingressConfig.UI.IngressClassName != "" {
		ingress.Spec.IngressClassName = &ingressConfig.UI.IngressClassName
	}

	for i := range ingressConfig.UI.TLS {
		ingress.Spec.TLS = append(ingress.Spec.TLS, v1beta1.IngressTLS{
			Hosts:      ingressConfig.UI.TLS[i].Hosts,
			SecretName: ingressConfig.UI.TLS[i].SecretName,
		})
	}

	return nil
}

func (b UIIngressBuilder) Placeholder() client.Object {
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

func getV1beta1IngressRule(host, serviceName string, servicePort intstr.IntOrString) v1beta1.IngressRule {
	return v1beta1.IngressRule{
		Host: host,
		IngressRuleValue: v1beta1.IngressRuleValue{
			HTTP: &v1beta1.HTTPIngressRuleValue{
				Paths: []v1beta1.HTTPIngressPath{
					{
						Backend: v1beta1.IngressBackend{
							ServiceName: serviceName,
							ServicePort: servicePort,
						},
					},
				},
			},
		},
	}
}

func getV1IngressRule(host, serviceName string, servicePort intstr.IntOrString) v1.IngressRule {
	pathType := v1.PathTypeImplementationSpecific
	return v1.IngressRule{
		Host: host,
		IngressRuleValue: v1.IngressRuleValue{
			HTTP: &v1.HTTPIngressRuleValue{
				Paths: []v1.HTTPIngressPath{
					{
						PathType: &pathType,
						Backend: v1.IngressBackend{
							Service: &v1.IngressServiceBackend{
								Name: serviceName,
								Port: v1.ServiceBackendPort{
									Name: servicePort.StrVal,
								},
							},
						},
					},
				},
			},
		},
	}
}

func (b *UIIngressBuilder) SSLPassThroughAnnotations() map[string]string {
	return map[string]string{
		"nginx.ingress.kubernetes.io/ssl-passthrough": "true",
	}
}

func (b *UIIngressBuilder) BackendProtocolAnnotations() map[string]string {
	return map[string]string{
		"nginx.ingress.kubernetes.io/backend-protocol": "HTTPS",
	}
}
