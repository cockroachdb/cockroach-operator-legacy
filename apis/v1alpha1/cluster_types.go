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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/api/networking/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// CrdbOperatorMigrationLabel is the label used to indicate that the cluster is migrating.
	// If this label is set to "true", the operator will stop reconciling the
	// entire cluster.
	CrdbOperatorMigrationLabel = "crdb.io/skip-reconcile"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.
// Important: Run "make dev/generate" to regenerate code after modifying this file

// +k8s:openapi-gen=true
// +k8s:deepcopy-gen=true

// CrdbClusterSpec defines the desired state of a CockroachDB Cluster
// that the operator maintains.
type CrdbClusterSpec struct {
	// Number of nodes (pods) in the cluster
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Number of nodes",xDescriptors="urn:alm:descriptor:com.tectonic.ui:podCount"
	// +kubebuilder:validation:Minimum=3
	// +required
	Nodes int32 `json:"nodes"`
	// (Optional) Container image information
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Cockroach Database Image"
	// +optional
	Image *PodImage `json:"image,omitempty"`
	// (Optional) The database port (`--port` CLI parameter when starting the service)
	// Default: 26258
	// +optional
	GRPCPort *int32 `json:"grpcPort,omitempty"`
	// (Optional) The web UI port (`--http-port` CLI parameter when starting the service)
	// Default: 8080
	// +optional
	HTTPPort *int32 `json:"httpPort,omitempty"`
	// (Optional) The SQL Port number
	// Default: 26257
	// +optional
	SQLPort *int32 `json:"sqlPort,omitempty"`
	// (Optional) TLSEnabled determines if TLS is enabled for your CockroachDB Cluster
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="TLS Enabled",xDescriptors="urn:alm:descriptor:com.tectonic.ui:booleanSwitch"
	// +optional
	TLSEnabled bool `json:"tlsEnabled,omitempty"`
	// (Optional) The secret with certificates and a private key for the TLS endpoint
	// on the database port. The standard naming of files is expected (tls.key, tls.crt, ca.crt)
	// Default: ""
	// +optional
	NodeTLSSecret string `json:"nodeTLSSecret,omitempty"`
	// (Optional) The secret with a certificate and a private key for root database user
	// Default: ""
	// +optional
	ClientTLSSecret string `json:"clientTLSSecret,omitempty"`
	// (Optional) The maximum number of pods that can be unavailable during a rolling update.
	// This number is set in the PodDistruptionBudget and defaults to 1.
	// +optional
	MaxUnavailable *int32 `json:"maxUnavailable,omitempty"`
	// (Optional) The min number of pods that can be unavailable during a rolling update.
	// This number is set in the PodDistruptionBudget and defaults to 1.
	// +optional
	MinAvailable *int32 `json:"minAvailable,omitempty"`
	// (Optional) The total size for caches (`--cache` command line parameter)
	// Default: "25%"
	// +optional
	Cache string `json:"cache,omitempty"`
	// (Optional) The maximum in-memory storage capacity available to store temporary
	// data for SQL queries (`--max-sql-memory` parameter)
	// Default: "25%"
	// +optional
	MaxSQLMemory string `json:"maxSQLMemory,omitempty"`
	// (Optional) Additional command line arguments for the `cockroach` binary
	// Default: ""
	// +optional
	AdditionalArgs []string `json:"additionalArgs,omitempty"`
	// (Optional) Database container resource limits. Any container limits
	// can be specified.
	// Default: (not specified)
	// +optional
	Resources corev1.ResourceRequirements `json:"resources,omitempty"`
	// Database disk storage configuration
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Data Store"
	// +required
	DataStore Volume `json:"dataStore"`
	// (Optional) CockroachDBVersion sets the explicit version of the cockroachDB image
	// Default: ""
	// +optional
	CockroachDBVersion string `json:"cockroachDBVersion,omitempty"`
	// (Optional) PriorityClassName sets the priority class of pods
	// Default: ""
	// +optional
	PriorityClassName string `json:"priorityClassName,omitempty"`
	// (Optional) PodEnvVariables is a slice of environment variables that are added to the pods
	// Default: (empty list)
	// +optional
	PodEnvVariables []corev1.EnvVar `json:"podEnvVariables,omitempty"`
	// (Optional) If specified, the pod's scheduling constraints
	// +optional
	Affinity *corev1.Affinity `json:"affinity,omitempty"`
	// (Optional) If specified, the pod's topology spread constraints
	// +optional
	TopologySpreadConstraints []corev1.TopologySpreadConstraint `json:"topologySpreadConstraints,omitempty"`
	// (Optional) Additional custom resource labels that are added to all resources
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Map of additional custom labels"
	// +optional
	AdditionalLabels map[string]string `json:"additionalLabels,omitempty"`
	// (Optional) Additional custom resource annotations that are added to all resources.
	// Changing `AdditionalAnnotations` field will result in cockroachDB cluster restart.
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Map of additional custom annotations"
	// +optional
	AdditionalAnnotations map[string]string `json:"additionalAnnotations,omitempty"`
	// (Optional) Tolerations for scheduling pods onto some dedicated nodes
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Cockroach Database Tolerations"
	// +optional
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
	// (Optional) If specified, the pod's nodeSelector
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Map of nodeSelectors to match when scheduling pods on nodes"
	// +optional
	NodeSelector map[string]string `json:"nodeSelector,omitempty"`
	// (Optional) HostAliases is an optional list of hosts and IPs that will be injected into the pod's hosts file if specified.
	// +optional
	HostAliases []corev1.HostAlias `json:"hostAliases,omitempty"`
	// (Optional) Ingress defines the Ingress configuration used to expose the services using Ingress
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Cockroach Database Ingress"
	// +optional
	Ingress *IngressConfig `json:"ingress,omitempty"`
	// (Optional) LogConfigMap define the config map which contains log configuration used to send the logs through the
	// proper channels in the cockroachdb. Logging configuration is available for cockroach version v21.1.0 onwards.
	// The logging configuration is taken in format of yaml file, you can check the logging configuration here (https://www.cockroachlabs.com/docs/stable/configure-logs.html#default-logging-configuration)
	// The default logging for cockroach version v20.x or less is stderr, logging API is ignored for older versions.
	// NOTE: The `data` field of map must contain an entry called `logging.yaml`
	// that contains config options.
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="Cockroach Database Logging configuration config map"
	// +optional
	LogConfigMap string `json:"logConfigMap,omitempty"`
	// (Optional) AutomountServiceAccountToken determines whether or not the stateful set pods should
	// automount the service account token. This is the default behavior in Kubernetes. For backward
	// compatibility reasons, this value defaults to `false` here.
	// Default: false
	// +optional
	AutomountServiceAccountToken bool `json:"automountServiceAccountToken,omitempty"`
	// (Optional) The grace period in seconds prior to the container being forcibly terminated
	// when marked for deletion or restarted.
	// Default : 300
	// +optional
	TerminationGracePeriodSecs int64 `json:"terminationGracePeriodSecs,omitempty"`
}

// +k8s:openapi-gen=true
// +k8s:deepcopy-gen=true

// CrdbClusterStatus defines the observed state of Cluster
type CrdbClusterStatus struct {
	// List of conditions representing the current status of the cluster resource.
	// +operator-sdk:csv:customresourcedefinitions:type=status, displayName="Cluster Conditions",xDescriptors="urn:alm:descriptor:io.kubernetes.conditions"
	Conditions []ClusterCondition `json:"conditions,omitempty"`
	// +operator-sdk:csv:customresourcedefinitions:type=status, displayName="Crdb Actions",xDescriptors="urn:alm:descriptor:com.tectonic.ui:hidden"
	OperatorActions []ClusterAction `json:"operatorActions,omitempty"`
	// Database service version. Not populated and is just a placeholder currently.
	// +operator-sdk:csv:customresourcedefinitions:type=status, displayName="Version",xDescriptors="urn:alm:descriptor:com.tectonic.ui:hidden"
	Version string `json:"version,omitempty"`
	// CrdbContainerImage is the container that will be installed
	// +operator-sdk:csv:customresourcedefinitions:type=status, displayName="CrdbContainerImage",xDescriptors="urn:alm:descriptor:com.tectonic.ui:hidden"
	CrdbContainerImage string `json:"crdbcontainerimage,omitempty"`
	// SQLHost is the host to be used with SQL ingress
	// +operator-sdk:csv:customresourcedefinitions:type=status, displayName="SQLHost",xDescriptors="urn:alm:descriptor:com.tectonic.ui:hidden"
	SQLHost string `json:"sqlHost,omitempty"`
	// OperatorStatus represent the status of the operator(Failed, Starting, Running or Other)
	// +operator-sdk:csv:customresourcedefinitions:type=status, displayName="OperatorStatus"
	ClusterStatus string `json:"clusterStatus,omitempty"`
}

// +k8s:openapi-gen=true
// +k8s:deepcopy-gen=true

// ClusterCondition represents cluster status as it is perceived by
// the operator
type ClusterCondition struct {
	// Type/Name of the condition
	// +required
	Type ClusterConditionType `json:"type"`
	// Condition status: True, False or Unknown
	// +required
	Status metav1.ConditionStatus `json:"status"`
	// The time when the condition was updated
	// +required
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`
}

// ClusterAction represents cluster status as it is perceived by
// the operator
// +k8s:deepcopy-gen=true
type ClusterAction struct {
	// Type/Name of the action
	// +required
	Type ActionType `json:"type"`
	// (Optional) Message related to the status of the action
	// +optional
	Message string `json:"message,omitempty"`
	// Action status: Failed, Finished or Unknown
	// +required
	Status string `json:"status"`
	// The time when the condition was updated
	// +required
	LastTransitionTime metav1.Time `json:"lastTransitionTime"`
}

// +genclient
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +k8s:deepcopy-gen=true
// +kubebuilder:deprecatedversion:warning="crdb.cockroachlabs.com/v1alpha1 CrdbCluster is deprecated; see https://www.cockroachlabs.com/docs/v26.2/kubernetes-deprecation-notice"
// +kubebuilder:object:root=true
// +kubebuilder:resource:categories=all;cockroachdb,shortName=crdb
// +kubebuilder:subresource:status
// +operator-sdk:csv:customresourcedefinitions:displayName="CockroachDB Operator"
// +k8s:openapi-gen=true

// CrdbCluster is the CRD for the cockroachDB clusters API
type CrdbCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CrdbClusterSpec   `json:"spec,omitempty"`
	Status CrdbClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:generate=true
// +k8s:openapi-gen=true
// +k8s:deepcopy-gen=true

// PodImage represents the image information for a container that is used
// to build the StatefulSet.
type PodImage struct {
	// Container image with supported CockroachDB version.
	// This defaults to the version pinned to the operator and requires a full container and tag/sha name.
	// For instance: cockroachdb/cockroachdb:v20.1
	// +required
	Name string `json:"name"`
	// (Optional) PullPolicy for the image, which defaults to IfNotPresent.
	// Default: IfNotPresent
	// +optional
	PullPolicyName *corev1.PullPolicy `json:"pullPolicy,omitempty"`
	// (Optional) Secret name containing the dockerconfig to use for a registry that requires authentication. The secret
	// must be configured first by the user.
	// +optional
	PullSecret *string `json:"pullSecret,omitempty"`
}

// +k8s:openapi-gen=true
// +kubebuilder:object:generate=true
// +k8s:deepcopy-gen=true

// Volume defined storage configuration for the container with the Database.
// Only one of the fields should set
type Volume struct {
	// (Optional) Directory from the host node's filesystem
	// +optional
	HostPath *corev1.HostPathVolumeSource `json:"hostPath,omitempty"`
	// (Optional) Persistent volume to use
	// +optional
	VolumeClaim *VolumeClaim `json:"pvc,omitempty"`
	// (Optional) SupportsAutoResize marks that a PVC will resize without restarting the entire cluster
	// Default: false
	// +operator-sdk:csv:customresourcedefinitions:type=spec,displayName="PVC Supports Auto Resizing",xDescriptors="urn:alm:descriptor:com.tectonic.ui:booleanSwitch"
	// +optional
	SupportsAutoResize bool `json:"supportsAutoResize"`
}

// +k8s:openapi-gen=true
// +kubebuilder:object:generate=true
// +k8s:deepcopy-gen=true

// IngressConfig defines the configuration required to create ingress resource
type IngressConfig struct {
	// (Optional) Ingress options for UI (HTTP) connections
	// +optional
	UI *Ingress `json:"ui,omitempty"`

	// (Optional) Ingress options for SQL connections
	// Adding/changing the SQL host will result in rolling update of the crdb cluster nodes
	// +optional
	SQL *Ingress `json:"sql,omitempty"`
}

// +kubebuilder:object:generate=true
// +k8s:openapi-gen=true
// +k8s:deepcopy-gen=true

type Ingress struct {
	// (Optional) IngressClassName to be used by ingress resource
	// +optional
	IngressClassName string `json:"ingressClassName,omitempty"`
	// (Optional) Annotations related to ingress resource
	// +optional
	Annotations map[string]string `json:"annotations,omitempty"`
	// (Optional) TLS describes the TLS certificate info
	// +optional
	TLS []v1.IngressTLS `json:"tls,omitempty"`
	// host is host to be used for exposing service
	// +required
	Host string `json:"host"`
}

// +kubebuilder:object:generate=true
// +k8s:openapi-gen=true
// +k8s:deepcopy-gen=true

// VolumeClaim wraps a persistent volume claim (PVC) to use with the container.
// Only one of the fields should set
type VolumeClaim struct {
	// (Optional) PVC to request a new persistent volume
	// +optional
	PersistentVolumeClaimSpec corev1.PersistentVolumeClaimSpec `json:"spec,omitempty"`
	// (Optional) Existing PVC in the same namespace
	// +optional
	PersistentVolumeSource corev1.PersistentVolumeClaimVolumeSource `json:"source,omitempty"`
}

// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object
// +kubebuilder:resource:categories=all;addons
// +k8s:deepcopy-gen=true

// CrdbClusterList contains a list of Cluster
type CrdbClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CrdbCluster `json:"items"`
}

func init() {
	SchemeBuilder.Register(&CrdbCluster{}, &CrdbClusterList{})
}
