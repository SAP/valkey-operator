/*
SPDX-FileCopyrightText: 2025 SAP SE or an SAP affiliate company and valkey-operator contributors
SPDX-License-Identifier: Apache-2.0
*/

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	runtime "k8s.io/apimachinery/pkg/runtime"

	prometheusv1 "github.com/prometheus-operator/prometheus-operator/pkg/apis/monitoring/v1"
	"github.com/sap/component-operator-runtime/pkg/component"
	componentoperatorruntimetypes "github.com/sap/component-operator-runtime/pkg/types"
)

// ValkeySpec defines the desired state of Valkey
type ValkeySpec struct {
	Version string `json:"version,omitempty"`
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:default=1
	Replicas                                int `json:"replicas,omitempty"`
	component.KubernetesPodProperties       `json:",inline"`
	component.KubernetesContainerProperties `json:",inline"`
	Sidecars                                []corev1.Container     `json:"sidecars,omitempty"`
	Sentinel                                *SentinelProperties    `json:"sentinel,omitempty"`
	Metrics                                 *MetricsProperties     `json:"metrics,omitempty"`
	TLS                                     *TLSProperties         `json:"tls,omitempty"`
	Persistence                             *PersistenceProperties `json:"persistence,omitempty"`
	Binding                                 *BindingProperties     `json:"binding,omitempty"`
	ExtraEnvVars                            []corev1.EnvVar        `json:"extraEnvVars,omitempty"`
}

// SentinelProperties models attributes of the sentinel sidecar
type SentinelProperties struct {
	Enabled                                 bool `json:"enabled,omitempty"`
	component.KubernetesContainerProperties `json:",inline"`
}

// MetricsProperties models attributes of the metrics exporter sidecar
type MetricsProperties struct {
	Enabled                                 bool `json:"enabled,omitempty"`
	component.KubernetesContainerProperties `json:",inline"`
	ServiceMonitor                          *MetricsServiceMonitorProperties `json:"monitor,omitempty"`
	PrometheusRule                          *MetricsPrometheusRuleProperties `json:"prometheusRule,omitempty"`
}

type MetricsServiceMonitorProperties struct {
	Enabled            bool                         `json:"enabled,omitempty"`
	Interval           prometheusv1.Duration        `json:"interval,omitempty"`
	ScrapeTimeout      prometheusv1.Duration        `json:"scrapeTimeout,omitempty"`
	Relabellings       []prometheusv1.RelabelConfig `json:"relabellings,omitempty"`
	MetricRelabellings []prometheusv1.RelabelConfig `json:"metricRelabelings,omitempty"`
	HonorLabels        bool                         `json:"honorLabels,omitempty"`
	AdditionalLabels   map[string]string            `json:"additionalLabels,omitempty"`
	PodTargetLabels    []string                     `json:"podTargetLabels,omitempty"`
}

type MetricsPrometheusRuleProperties struct {
	Enabled          bool                `json:"enabled,omitempty"`
	AdditionalLabels map[string]string   `json:"additionalLabels,omitempty"`
	Rules            []prometheusv1.Rule `json:"rules,omitempty"`
}

// TLSProperties models TLS settings of the valkey services
type TLSProperties struct {
	Enabled     bool                   `json:"enabled,omitempty"`
	CertManager *CertManagerProperties `json:"certManager,omitempty"`
}

// CertManagerProperties models cert-manager related attributes
type CertManagerProperties struct {
	Issuer *ObjectReference `json:"issuer,omitempty"`
}

// ObjectReference models a reference to a Kubernetes object
type ObjectReference struct {
	Group string `json:"group,omitempty"`
	Kind  string `json:"kind,omitempty"`
	Name  string `json:"name,omitempty"`
}

// PersistenceProperties models persistence related attributes
type PersistenceProperties struct {
	Enabled      bool               `json:"enabled,omitempty"`
	Size         *resource.Quantity `json:"size,omitempty"`
	StorageClass string             `json:"storageClass,omitempty"`
	ExtraVolumes []corev1.Volume    `json:"extraVolumes,omitempty"`
}

// BindingProperties models custom properties for the generated binding secret
type BindingProperties struct {
	SecretName string  `json:"secretName,omitempty"`
	Template   *string `json:"template,omitempty"`
}

// ValkeyStatus defines the observed state of Valkey
type ValkeyStatus struct {
	component.Status `json:",inline"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="State",type=string,JSONPath=`.status.state`
// +kubebuilder:printcolumn:name="Age",type="date",JSONPath=".metadata.creationTimestamp"
// +genclient

// Valkey is the Schema for the valkey API
type Valkey struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ValkeySpec `json:"spec,omitempty"`
	// +kubebuilder:default={"observedGeneration":-1}
	Status ValkeyStatus `json:"status,omitempty"`
}

var _ component.Component = &Valkey{}

// +kubebuilder:object:root=true

// ValkeyList contains a list of Valkey
type ValkeyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Valkey `json:"items"`
}

func (s *ValkeySpec) ToUnstructured() map[string]any {
	result, err := runtime.DefaultUnstructuredConverter.ToUnstructured(s)
	if err != nil {
		panic(err)
	}
	return result
}

func (c *Valkey) GetDeploymentNamespace() string {
	return c.Namespace
}

func (c *Valkey) GetDeploymentName() string {
	return c.Name
}

func (c *Valkey) GetSpec() componentoperatorruntimetypes.Unstructurable {
	return &c.Spec
}

func (c *Valkey) GetStatus() *component.Status {
	return &c.Status.Status
}

func init() {
	SchemeBuilder.Register(&Valkey{}, &ValkeyList{})
}
